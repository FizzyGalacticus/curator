package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// MediaType distinguishes image, gif, and video media items.
type MediaType string

const (
	MediaImage MediaType = "image"
	MediaVideo MediaType = "video"
	MediaGif   MediaType = "gif"
)

// MediaItem represents one piece of media within a Reddit post.
type MediaItem struct {
	Type      MediaType `json:"type"`
	URL       string    `json:"url"`                 // full-size URL shown in viewer
	Thumbnail string    `json:"thumbnail,omitempty"` // smaller URL used in the grid
	Width     int       `json:"width,omitempty"`
	Height    int       `json:"height,omitempty"`
}

// Post is a Reddit post that contains at least one MediaItem.
type Post struct {
	ID           string      `json:"id"`
	Subreddit    string      `json:"subreddit"`
	Title        string      `json:"title"`
	Author       string      `json:"author"`
	Score        int         `json:"score"`
	CreatedAt    time.Time   `json:"created_at"`
	Permalink    string      `json:"permalink"`
	MediaItems   []MediaItem `json:"media_items"`
	DiscoveredAt time.Time   `json:"discovered_at"`
}

// StorageData is the on-disk JSON schema for data.json.
type StorageData struct {
	Posts       []Post               `json:"posts"`
	Favorites   map[string]bool      `json:"favorites"`    // post ID -> true
	LastChecked map[string]time.Time `json:"last_checked"` // subreddit -> time
}

// Storage manages persisted data with thread-safe operations.
type Storage struct {
	mu        sync.RWMutex
	filePath  string
	data      StorageData
	mediaURLs map[string]bool // index of primary media URLs for dedup
}

// NewStorage loads or creates a Storage instance.
func NewStorage(filePath string) (*Storage, error) {
	s := &Storage{
		filePath:  filePath,
		mediaURLs: map[string]bool{},
		data: StorageData{
			Posts:       []Post{},
			Favorites:   map[string]bool{},
			LastChecked: map[string]time.Time{},
		},
	}
	if err := s.load(); err != nil {
		if os.IsNotExist(err) {
			return s, s.save()
		}
		return nil, err
	}
	return s, nil
}

// rebuildURLIndex regenerates the in-memory primary-URL index from current posts.
// Must be called with s.mu held.
func (s *Storage) rebuildURLIndex() {
	s.mediaURLs = make(map[string]bool, len(s.data.Posts))
	for _, p := range s.data.Posts {
		if len(p.MediaItems) > 0 {
			s.mediaURLs[p.MediaItems[0].URL] = true
		}
	}
}

func (s *Storage) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &s.data); err != nil {
		return err
	}
	if s.data.Favorites == nil {
		s.data.Favorites = map[string]bool{}
	}
	if s.data.LastChecked == nil {
		s.data.LastChecked = map[string]time.Time{}
	}
	if s.data.Posts == nil {
		s.data.Posts = []Post{}
	}
	s.rebuildURLIndex()
	return nil
}

func (s *Storage) save() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, data, 0644)
}

// AddPosts inserts new posts, skipping any whose ID already exists or whose
// primary media URL matches an already-stored post (catches cross-posts with
// different IDs but the same underlying media).
func (s *Storage) AddPosts(posts []Post) error {
	if len(posts) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	existingIDs := make(map[string]bool, len(s.data.Posts))
	for _, p := range s.data.Posts {
		existingIDs[p.ID] = true
	}

	changed := false
	for _, p := range posts {
		if existingIDs[p.ID] {
			continue
		}
		if len(p.MediaItems) > 0 && s.mediaURLs[p.MediaItems[0].URL] {
			continue
		}
		s.data.Posts = append(s.data.Posts, p)
		existingIDs[p.ID] = true
		if len(p.MediaItems) > 0 {
			s.mediaURLs[p.MediaItems[0].URL] = true
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.save()
}

// GetPosts returns a copy of all stored posts.
func (s *Storage) GetPosts() []Post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Post, len(s.data.Posts))
	copy(out, s.data.Posts)
	return out
}

// GetFavorites returns a copy of the favorites map.
func (s *Storage) GetFavorites() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.data.Favorites))
	for k, v := range s.data.Favorites {
		out[k] = v
	}
	return out
}

// ToggleFavorite flips the favorite state for a post and returns the new state.
func (s *Storage) ToggleFavorite(postID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	nowFav := !s.data.Favorites[postID]
	if nowFav {
		s.data.Favorites[postID] = true
	} else {
		delete(s.data.Favorites, postID)
	}
	return nowFav, s.save()
}

// SetFavorite sets the favorite state for a post to the given value.
func (s *Storage) SetFavorite(postID string, fav bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if fav {
		s.data.Favorites[postID] = true
	} else {
		delete(s.data.Favorites, postID)
	}
	return s.save()
}

// GetLastChecked returns the last time a subreddit was checked.
func (s *Storage) GetLastChecked(subreddit string) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.LastChecked[subreddit]
}

// SetLastChecked updates the last-checked timestamp for a subreddit.
func (s *Storage) SetLastChecked(subreddit string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.LastChecked[subreddit] = t
	return s.save()
}

// RemoveSubredditData deletes non-favorited posts for a subreddit and clears its last-checked time.
func (s *Storage) RemoveSubredditData(subreddit string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var kept []Post
	for _, p := range s.data.Posts {
		if p.Subreddit != subreddit || s.data.Favorites[p.ID] {
			kept = append(kept, p)
		}
	}
	if kept == nil {
		kept = []Post{}
	}
	s.data.Posts = kept
	delete(s.data.LastChecked, subreddit)
	s.rebuildURLIndex()
	return s.save()
}

// PruneOldPosts removes non-favorited posts older than maxAgeDays.
func (s *Storage) PruneOldPosts(maxAgeDays int) error {
	if maxAgeDays <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)

	s.mu.Lock()
	defer s.mu.Unlock()

	var kept []Post
	changed := false
	for _, p := range s.data.Posts {
		if s.data.Favorites[p.ID] || !p.CreatedAt.Before(cutoff) {
			kept = append(kept, p)
		} else {
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if kept == nil {
		kept = []Post{}
	}
	s.data.Posts = kept
	s.rebuildURLIndex()
	return s.save()
}
