package main

import (
	"path/filepath"
	"testing"
	"time"
)

// newTestStorage creates a Storage backed by a temp file for use in tests.
func newTestStorage(t *testing.T) *Storage {
	t.Helper()
	s, err := NewStorage(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	return s
}

func makePost(id, subreddit string, age time.Duration) Post {
	return Post{
		ID:         id,
		Subreddit:  subreddit,
		Title:      "Post " + id,
		CreatedAt:  time.Now().UTC().Add(-age),
		MediaItems: []MediaItem{{Type: MediaImage, URL: "https://example.com/" + id + ".jpg"}},
	}
}

// ── NewStorage ────────────────────────────────────────────────────────────

func TestNewStorage_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")

	s, err := NewStorage(path)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil storage")
	}

	posts := s.GetPosts()
	if len(posts) != 0 {
		t.Errorf("expected empty posts, got %d", len(posts))
	}
}

func TestNewStorage_LoadsExisting(t *testing.T) {
	s := newTestStorage(t)
	if err := s.AddPosts([]Post{makePost("p1", "pics", 0)}); err != nil {
		t.Fatalf("AddPosts: %v", err)
	}

	// Reload from same path — should see the post.
	s2, err := NewStorage(s.filePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := len(s2.GetPosts()); got != 1 {
		t.Errorf("want 1 post after reload, got %d", got)
	}
}

// ── AddPosts ──────────────────────────────────────────────────────────────

func TestStorage_AddPosts_Basic(t *testing.T) {
	s := newTestStorage(t)
	posts := []Post{makePost("a", "pics", 0), makePost("b", "pics", 0)}

	if err := s.AddPosts(posts); err != nil {
		t.Fatalf("AddPosts: %v", err)
	}
	if got := len(s.GetPosts()); got != 2 {
		t.Errorf("want 2 posts, got %d", got)
	}
}

func TestStorage_AddPosts_Dedup(t *testing.T) {
	s := newTestStorage(t)
	p := makePost("dup", "pics", 0)

	s.AddPosts([]Post{p})
	s.AddPosts([]Post{p}) // same ID again

	if got := len(s.GetPosts()); got != 1 {
		t.Errorf("want 1 post after dedup, got %d", got)
	}
}

func TestStorage_AddPosts_URLDedup(t *testing.T) {
	s := newTestStorage(t)

	// Two posts with different IDs but the same primary media URL (cross-post scenario).
	p1 := makePost("id1", "pics", 0)
	p2 := makePost("id2", "funny", 0) // different subreddit/ID
	p2.MediaItems[0].URL = p1.MediaItems[0].URL

	s.AddPosts([]Post{p1})
	s.AddPosts([]Post{p2})

	if got := len(s.GetPosts()); got != 1 {
		t.Errorf("want 1 post after URL dedup, got %d", got)
	}
}

func TestStorage_AddPosts_URLDedup_RebuildAfterRemove(t *testing.T) {
	s := newTestStorage(t)

	p1 := makePost("id1", "pics", 0)
	p2 := makePost("id2", "pics", 0)
	p2.MediaItems[0].URL = p1.MediaItems[0].URL // same URL

	s.AddPosts([]Post{p1, p2}) // only p1 should be stored

	if got := len(s.GetPosts()); got != 1 {
		t.Fatalf("setup: want 1 post, got %d", got)
	}

	// After removing the subreddit, the URL index should clear, allowing
	// a post with that URL to be stored again.
	s.RemoveSubredditData("pics")

	if got := len(s.GetPosts()); got != 0 {
		t.Fatalf("after remove: want 0 posts, got %d", got)
	}

	s.AddPosts([]Post{p2})
	if got := len(s.GetPosts()); got != 1 {
		t.Errorf("after re-add: want 1 post, got %d", got)
	}
}

func TestStorage_AddPosts_Empty(t *testing.T) {
	s := newTestStorage(t)
	if err := s.AddPosts(nil); err != nil {
		t.Fatalf("AddPosts(nil): %v", err)
	}
	if err := s.AddPosts([]Post{}); err != nil {
		t.Fatalf("AddPosts([]): %v", err)
	}
}

// ── GetPosts ──────────────────────────────────────────────────────────────

func TestStorage_GetPosts_ReturnsCopy(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts([]Post{makePost("x", "pics", 0)})

	got := s.GetPosts()
	got[0].Title = "mutated"

	original := s.GetPosts()
	if original[0].Title == "mutated" {
		t.Error("GetPosts should return a copy, not a reference")
	}
}

// ── ToggleFavorite / SetFavorite ──────────────────────────────────────────

func TestStorage_ToggleFavorite(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts([]Post{makePost("p1", "pics", 0)})

	nowFav, err := s.ToggleFavorite("p1")
	if err != nil {
		t.Fatalf("ToggleFavorite: %v", err)
	}
	if !nowFav {
		t.Error("first toggle should mark as favorite")
	}

	favs := s.GetFavorites()
	if !favs["p1"] {
		t.Error("p1 should be in favorites")
	}

	// Toggle back off.
	nowFav, err = s.ToggleFavorite("p1")
	if err != nil {
		t.Fatalf("second ToggleFavorite: %v", err)
	}
	if nowFav {
		t.Error("second toggle should unmark favorite")
	}
	if s.GetFavorites()["p1"] {
		t.Error("p1 should no longer be in favorites")
	}
}

func TestStorage_SetFavorite(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts([]Post{makePost("p2", "pics", 0)})

	if err := s.SetFavorite("p2", true); err != nil {
		t.Fatalf("SetFavorite true: %v", err)
	}
	if !s.GetFavorites()["p2"] {
		t.Error("p2 should be favorited")
	}

	if err := s.SetFavorite("p2", false); err != nil {
		t.Fatalf("SetFavorite false: %v", err)
	}
	if s.GetFavorites()["p2"] {
		t.Error("p2 should no longer be favorited")
	}
}

// ── LastChecked ───────────────────────────────────────────────────────────

func TestStorage_LastChecked_RoundTrip(t *testing.T) {
	s := newTestStorage(t)

	// Zero value for unknown subreddit.
	if got := s.GetLastChecked("nosub"); !got.IsZero() {
		t.Errorf("unknown subreddit: want zero time, got %v", got)
	}

	ts := time.Now().UTC().Truncate(time.Second)
	if err := s.SetLastChecked("pics", ts); err != nil {
		t.Fatalf("SetLastChecked: %v", err)
	}
	if got := s.GetLastChecked("pics"); !got.Equal(ts) {
		t.Errorf("want %v, got %v", ts, got)
	}
}

// ── RemoveSubredditData ───────────────────────────────────────────────────

func TestStorage_RemoveSubredditData(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts([]Post{
		makePost("a", "pics", 0),
		makePost("b", "pics", 0),
		makePost("c", "videos", 0),
	})
	s.SetLastChecked("pics", time.Now().UTC())

	if err := s.RemoveSubredditData("pics"); err != nil {
		t.Fatalf("RemoveSubredditData: %v", err)
	}

	posts := s.GetPosts()
	if len(posts) != 1 || posts[0].ID != "c" {
		t.Errorf("want only post c remaining, got %v", posts)
	}
	if !s.GetLastChecked("pics").IsZero() {
		t.Error("last_checked for removed subreddit should be zero")
	}
}

func TestStorage_RemoveSubredditData_PreservesFavorites(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts([]Post{
		makePost("fav", "pics", 0),
		makePost("nonfav", "pics", 0),
	})
	s.SetFavorite("fav", true)

	s.RemoveSubredditData("pics")

	posts := s.GetPosts()
	if len(posts) != 1 || posts[0].ID != "fav" {
		t.Errorf("favorited post should survive removal, got %v", posts)
	}
}

// ── PruneOldPosts ─────────────────────────────────────────────────────────

func TestStorage_PruneOldPosts(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts([]Post{
		makePost("old", "pics", 40*24*time.Hour),  // 40 days old
		makePost("new", "pics", 5*24*time.Hour),   // 5 days old
		makePost("fav", "pics", 40*24*time.Hour),  // 40 days old but favorited
	})
	s.SetFavorite("fav", true)

	if err := s.PruneOldPosts(30); err != nil {
		t.Fatalf("PruneOldPosts: %v", err)
	}

	ids := map[string]bool{}
	for _, p := range s.GetPosts() {
		ids[p.ID] = true
	}
	if ids["old"] {
		t.Error("old non-favorited post should be pruned")
	}
	if !ids["new"] {
		t.Error("new post should be kept")
	}
	if !ids["fav"] {
		t.Error("old favorited post should be kept")
	}
}

func TestStorage_PruneOldPosts_SkipsWhenDisabled(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts([]Post{makePost("ancient", "pics", 365*24*time.Hour)})

	if err := s.PruneOldPosts(0); err != nil {
		t.Fatalf("PruneOldPosts(0): %v", err)
	}
	if len(s.GetPosts()) != 1 {
		t.Error("maxAgeDays=0 should disable pruning")
	}
}
