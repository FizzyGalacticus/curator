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

// PostSource identifies which upstream platform a Post came from.
type PostSource string

const (
	SourceReddit PostSource = "reddit" // includes Scrolller, which mirrors Reddit subreddit content
	SourceFlickr PostSource = "flickr"
	SourceLemmy  PostSource = "lemmy"
)

// MediaItem represents one piece of media within a Reddit post.
type MediaItem struct {
	Type      MediaType `json:"type"`
	URL       string    `json:"url"`                 // full-size URL shown in viewer
	Thumbnail string    `json:"thumbnail,omitempty"` // smaller URL used in the grid
	Width     int       `json:"width,omitempty"`
	Height    int       `json:"height,omitempty"`
}

// Post is a single piece of curated content, sourced from Reddit, Flickr, or
// Lemmy. Subreddit holds the origin identifier within that source: a
// subreddit name, a Flickr group slug, or a Lemmy "community@instance" string.
type Post struct {
	ID           string      `json:"id"`
	Source       PostSource  `json:"source"`
	Subreddit    string      `json:"subreddit"`
	Title        string      `json:"title"`
	Author       string      `json:"author"`
	Score        int         `json:"score"`
	CreatedAt    time.Time   `json:"created_at"`
	Permalink    string      `json:"permalink"`
	MediaItems   []MediaItem `json:"media_items"`
	DiscoveredAt time.Time   `json:"discovered_at"`
}

// ListData is the on-disk JSON schema for one curation list's data.
type ListData struct {
	Posts       []Post               `json:"posts"`
	Favorites   map[string]bool      `json:"favorites"`    // post ID -> true
	LastChecked map[string]time.Time `json:"last_checked"` // "source:name" -> time
}

// lastCheckedKey namespaces a LastChecked entry by source so the same literal
// identifier under two different sources (e.g. a Flickr group and a Reddit
// subreddit both named "pics") never collide.
func lastCheckedKey(source PostSource, name string) string {
	return string(source) + ":" + name
}

// StorageData is the on-disk JSON schema for data.json.
type StorageData struct {
	Lists map[string]*ListData `json:"lists"` // list ID -> data
}

// Storage manages persisted data with thread-safe operations.
type Storage struct {
	mu        sync.RWMutex
	filePath  string
	data      StorageData
	mediaURLs map[string]map[string]bool // list ID -> index of primary media URLs for dedup
}

// NewStorage loads or creates a Storage instance.
func NewStorage(filePath string) (*Storage, error) {
	s := &Storage{
		filePath:  filePath,
		mediaURLs: map[string]map[string]bool{},
		data: StorageData{
			Lists: map[string]*ListData{},
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

func newListData() *ListData {
	return &ListData{
		Posts:       []Post{},
		Favorites:   map[string]bool{},
		LastChecked: map[string]time.Time{},
	}
}

// rebuildURLIndex regenerates the in-memory primary-URL index from current posts.
// Must be called with s.mu held.
func (s *Storage) rebuildURLIndex() {
	s.mediaURLs = make(map[string]map[string]bool, len(s.data.Lists))
	for listID, ld := range s.data.Lists {
		urls := make(map[string]bool, len(ld.Posts))
		for _, p := range ld.Posts {
			if len(p.MediaItems) > 0 {
				urls[p.MediaItems[0].URL] = true
			}
		}
		s.mediaURLs[listID] = urls
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
	if s.data.Lists == nil {
		s.data.Lists = map[string]*ListData{}
	}
	for _, ld := range s.data.Lists {
		if ld.Favorites == nil {
			ld.Favorites = map[string]bool{}
		}
		if ld.LastChecked == nil {
			ld.LastChecked = map[string]time.Time{}
		}
		if ld.Posts == nil {
			ld.Posts = []Post{}
		}
		for i, p := range ld.Posts {
			if p.Source == "" {
				ld.Posts[i].Source = SourceReddit
			}
		}
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

// getOrCreateList returns the ListData for listID, creating it (and its URL
// index) if this is the first write. Must be called with s.mu held (write lock).
func (s *Storage) getOrCreateList(listID string) *ListData {
	ld, ok := s.data.Lists[listID]
	if !ok {
		ld = newListData()
		s.data.Lists[listID] = ld
	}
	if s.mediaURLs[listID] == nil {
		s.mediaURLs[listID] = map[string]bool{}
	}
	return ld
}

// AddPosts inserts new posts into a list, skipping any whose ID already exists
// or whose primary media URL matches an already-stored post in that same list
// (catches cross-posts with different IDs but the same underlying media).
// Dedup is scoped per list — the same post/URL may exist independently in
// multiple lists.
func (s *Storage) AddPosts(listID string, posts []Post) error {
	if len(posts) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ld := s.getOrCreateList(listID)
	urls := s.mediaURLs[listID]

	existingIDs := make(map[string]bool, len(ld.Posts))
	for _, p := range ld.Posts {
		existingIDs[p.ID] = true
	}

	changed := false
	for _, p := range posts {
		if existingIDs[p.ID] {
			continue
		}
		if len(p.MediaItems) > 0 && urls[p.MediaItems[0].URL] {
			continue
		}
		ld.Posts = append(ld.Posts, p)
		existingIDs[p.ID] = true
		if len(p.MediaItems) > 0 {
			urls[p.MediaItems[0].URL] = true
		}
		changed = true
	}
	if !changed {
		return nil
	}
	return s.save()
}

// GetPosts returns a copy of all posts stored for a list. Unknown list IDs
// return an empty slice without creating a bucket for them.
func (s *Storage) GetPosts(listID string) []Post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ld, ok := s.data.Lists[listID]
	if !ok {
		return []Post{}
	}
	out := make([]Post, len(ld.Posts))
	copy(out, ld.Posts)
	return out
}

// GetFavorites returns a copy of a list's favorites map. Unknown list IDs
// return an empty map without creating a bucket for them.
func (s *Storage) GetFavorites(listID string) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ld, ok := s.data.Lists[listID]
	if !ok {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(ld.Favorites))
	for k, v := range ld.Favorites {
		out[k] = v
	}
	return out
}

// ToggleFavorite flips the favorite state for a post within a list and
// returns the new state.
func (s *Storage) ToggleFavorite(listID, postID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ld := s.getOrCreateList(listID)
	nowFav := !ld.Favorites[postID]
	if nowFav {
		ld.Favorites[postID] = true
	} else {
		delete(ld.Favorites, postID)
	}
	return nowFav, s.save()
}

// SetFavorite sets the favorite state for a post within a list to the given value.
func (s *Storage) SetFavorite(listID, postID string, fav bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ld := s.getOrCreateList(listID)
	if fav {
		ld.Favorites[postID] = true
	} else {
		delete(ld.Favorites, postID)
	}
	return s.save()
}

// GetLastChecked returns the last time a source identifier was checked within
// a list. Unknown list IDs return the zero time without creating a bucket for them.
func (s *Storage) GetLastChecked(listID string, source PostSource, name string) time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ld, ok := s.data.Lists[listID]
	if !ok {
		return time.Time{}
	}
	return ld.LastChecked[lastCheckedKey(source, name)]
}

// SetLastChecked updates the last-checked timestamp for a source identifier within a list.
func (s *Storage) SetLastChecked(listID string, source PostSource, name string, t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ld := s.getOrCreateList(listID)
	ld.LastChecked[lastCheckedKey(source, name)] = t
	return s.save()
}

// RemoveSourceData deletes non-favorited posts for a source identifier within
// a list and clears its last-checked time. Matches on both source and name so
// the same literal identifier under a different source is left untouched.
// No-op if the list doesn't exist.
func (s *Storage) RemoveSourceData(listID string, source PostSource, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ld, ok := s.data.Lists[listID]
	if !ok {
		return nil
	}

	var kept []Post
	for _, p := range ld.Posts {
		if p.Source != source || p.Subreddit != name || ld.Favorites[p.ID] {
			kept = append(kept, p)
		}
	}
	if kept == nil {
		kept = []Post{}
	}
	ld.Posts = kept
	delete(ld.LastChecked, lastCheckedKey(source, name))
	s.rebuildURLIndex()
	return s.save()
}

// PruneOldPosts removes non-favorited posts older than maxAgeDays within a list.
func (s *Storage) PruneOldPosts(listID string, maxAgeDays int) error {
	if maxAgeDays <= 0 {
		return nil
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)

	s.mu.Lock()
	defer s.mu.Unlock()

	ld, ok := s.data.Lists[listID]
	if !ok {
		return nil
	}

	var kept []Post
	changed := false
	for _, p := range ld.Posts {
		if ld.Favorites[p.ID] || !p.CreatedAt.Before(cutoff) {
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
	ld.Posts = kept
	s.rebuildURLIndex()
	return s.save()
}

// RemoveList permanently deletes all data (posts, favorites, last-checked)
// for a curation list. Idempotent — no error if the list doesn't exist.
func (s *Storage) RemoveList(listID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Lists[listID]; !ok {
		return nil
	}
	delete(s.data.Lists, listID)
	delete(s.mediaURLs, listID)
	return s.save()
}
