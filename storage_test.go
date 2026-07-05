package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testListID = "list1"

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
	return makePostSource(id, SourceReddit, subreddit, age)
}

func makePostSource(id string, source PostSource, subreddit string, age time.Duration) Post {
	return Post{
		ID:         id,
		Source:     source,
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

	posts := s.GetPosts(testListID)
	if len(posts) != 0 {
		t.Errorf("expected empty posts, got %d", len(posts))
	}
}

func TestNewStorage_LoadsExisting(t *testing.T) {
	s := newTestStorage(t)
	if err := s.AddPosts(testListID, []Post{makePost("p1", "pics", 0)}); err != nil {
		t.Fatalf("AddPosts: %v", err)
	}

	// Reload from same path — should see the post.
	s2, err := NewStorage(s.filePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := len(s2.GetPosts(testListID)); got != 1 {
		t.Errorf("want 1 post after reload, got %d", got)
	}
}

func TestNewStorage_BackfillsEmptySourceToReddit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.json")
	fixture := `{"lists":{"list1":{"posts":[{"id":"p1","subreddit":"pics","media_items":[{"type":"image","url":"https://example.com/p1.jpg"}]}],"favorites":{},"last_checked":{}}}}`
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	s, err := NewStorage(path)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	posts := s.GetPosts("list1")
	if len(posts) != 1 {
		t.Fatalf("want 1 post, got %d", len(posts))
	}
	if posts[0].Source != SourceReddit {
		t.Errorf("want Source backfilled to %q, got %q", SourceReddit, posts[0].Source)
	}
}

// ── AddPosts ──────────────────────────────────────────────────────────────

func TestStorage_AddPosts_Basic(t *testing.T) {
	s := newTestStorage(t)
	posts := []Post{makePost("a", "pics", 0), makePost("b", "pics", 0)}

	if err := s.AddPosts(testListID, posts); err != nil {
		t.Fatalf("AddPosts: %v", err)
	}
	if got := len(s.GetPosts(testListID)); got != 2 {
		t.Errorf("want 2 posts, got %d", got)
	}
}

func TestStorage_AddPosts_Dedup(t *testing.T) {
	s := newTestStorage(t)
	p := makePost("dup", "pics", 0)

	s.AddPosts(testListID, []Post{p})
	s.AddPosts(testListID, []Post{p}) // same ID again

	if got := len(s.GetPosts(testListID)); got != 1 {
		t.Errorf("want 1 post after dedup, got %d", got)
	}
}

func TestStorage_AddPosts_URLDedup(t *testing.T) {
	s := newTestStorage(t)

	// Two posts with different IDs but the same primary media URL (cross-post scenario).
	p1 := makePost("id1", "pics", 0)
	p2 := makePost("id2", "funny", 0) // different subreddit/ID
	p2.MediaItems[0].URL = p1.MediaItems[0].URL

	s.AddPosts(testListID, []Post{p1})
	s.AddPosts(testListID, []Post{p2})

	if got := len(s.GetPosts(testListID)); got != 1 {
		t.Errorf("want 1 post after URL dedup, got %d", got)
	}
}

func TestStorage_AddPosts_URLDedup_RebuildAfterRemove(t *testing.T) {
	s := newTestStorage(t)

	p1 := makePost("id1", "pics", 0)
	p2 := makePost("id2", "pics", 0)
	p2.MediaItems[0].URL = p1.MediaItems[0].URL // same URL

	s.AddPosts(testListID, []Post{p1, p2}) // only p1 should be stored

	if got := len(s.GetPosts(testListID)); got != 1 {
		t.Fatalf("setup: want 1 post, got %d", got)
	}

	// After removing the subreddit, the URL index should clear, allowing
	// a post with that URL to be stored again.
	s.RemoveSourceData(testListID, SourceReddit, "pics")

	if got := len(s.GetPosts(testListID)); got != 0 {
		t.Fatalf("after remove: want 0 posts, got %d", got)
	}

	s.AddPosts(testListID, []Post{p2})
	if got := len(s.GetPosts(testListID)); got != 1 {
		t.Errorf("after re-add: want 1 post, got %d", got)
	}
}

func TestStorage_AddPosts_Empty(t *testing.T) {
	s := newTestStorage(t)
	if err := s.AddPosts(testListID, nil); err != nil {
		t.Fatalf("AddPosts(nil): %v", err)
	}
	if err := s.AddPosts(testListID, []Post{}); err != nil {
		t.Fatalf("AddPosts([]): %v", err)
	}
}

func TestStorage_AddPosts_MixedSources(t *testing.T) {
	s := newTestStorage(t)
	posts := []Post{
		makePostSource("r1", SourceReddit, "pics", 0),
		makePostSource("f1", SourceFlickr, "blackandwhite", 0),
		makePostSource("l1", SourceLemmy, "pics@lemmy.world", 0),
	}
	if err := s.AddPosts(testListID, posts); err != nil {
		t.Fatalf("AddPosts: %v", err)
	}

	stored := s.GetPosts(testListID)
	if len(stored) != 3 {
		t.Fatalf("want 3 posts, got %d", len(stored))
	}
	bySource := map[PostSource]bool{}
	for _, p := range stored {
		bySource[p.Source] = true
	}
	for _, want := range []PostSource{SourceReddit, SourceFlickr, SourceLemmy} {
		if !bySource[want] {
			t.Errorf("expected a post with source %q", want)
		}
	}
}

// ── GetPosts ──────────────────────────────────────────────────────────────

func TestStorage_GetPosts_ReturnsCopy(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts(testListID, []Post{makePost("x", "pics", 0)})

	got := s.GetPosts(testListID)
	got[0].Title = "mutated"

	original := s.GetPosts(testListID)
	if original[0].Title == "mutated" {
		t.Error("GetPosts should return a copy, not a reference")
	}
}

// ── ToggleFavorite / SetFavorite ──────────────────────────────────────────

func TestStorage_ToggleFavorite(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts(testListID, []Post{makePost("p1", "pics", 0)})

	nowFav, err := s.ToggleFavorite(testListID, "p1")
	if err != nil {
		t.Fatalf("ToggleFavorite: %v", err)
	}
	if !nowFav {
		t.Error("first toggle should mark as favorite")
	}

	favs := s.GetFavorites(testListID)
	if !favs["p1"] {
		t.Error("p1 should be in favorites")
	}

	// Toggle back off.
	nowFav, err = s.ToggleFavorite(testListID, "p1")
	if err != nil {
		t.Fatalf("second ToggleFavorite: %v", err)
	}
	if nowFav {
		t.Error("second toggle should unmark favorite")
	}
	if s.GetFavorites(testListID)["p1"] {
		t.Error("p1 should no longer be in favorites")
	}
}

func TestStorage_SetFavorite(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts(testListID, []Post{makePost("p2", "pics", 0)})

	if err := s.SetFavorite(testListID, "p2", true); err != nil {
		t.Fatalf("SetFavorite true: %v", err)
	}
	if !s.GetFavorites(testListID)["p2"] {
		t.Error("p2 should be favorited")
	}

	if err := s.SetFavorite(testListID, "p2", false); err != nil {
		t.Fatalf("SetFavorite false: %v", err)
	}
	if s.GetFavorites(testListID)["p2"] {
		t.Error("p2 should no longer be favorited")
	}
}

// ── LastChecked ───────────────────────────────────────────────────────────

func TestStorage_LastChecked_RoundTrip(t *testing.T) {
	s := newTestStorage(t)

	// Zero value for unknown subreddit.
	if got := s.GetLastChecked(testListID, SourceReddit, "nosub"); !got.IsZero() {
		t.Errorf("unknown subreddit: want zero time, got %v", got)
	}

	ts := time.Now().UTC().Truncate(time.Second)
	if err := s.SetLastChecked(testListID, SourceReddit, "pics", ts); err != nil {
		t.Fatalf("SetLastChecked: %v", err)
	}
	if got := s.GetLastChecked(testListID, SourceReddit, "pics"); !got.Equal(ts) {
		t.Errorf("want %v, got %v", ts, got)
	}
}

func TestStorage_LastChecked_NamespacedBySource(t *testing.T) {
	s := newTestStorage(t)

	redditTS := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	flickrTS := time.Now().UTC().Truncate(time.Second)

	if err := s.SetLastChecked(testListID, SourceReddit, "pics", redditTS); err != nil {
		t.Fatalf("SetLastChecked(reddit): %v", err)
	}
	if err := s.SetLastChecked(testListID, SourceFlickr, "pics", flickrTS); err != nil {
		t.Fatalf("SetLastChecked(flickr): %v", err)
	}

	if got := s.GetLastChecked(testListID, SourceReddit, "pics"); !got.Equal(redditTS) {
		t.Errorf("reddit \"pics\": want %v, got %v", redditTS, got)
	}
	if got := s.GetLastChecked(testListID, SourceFlickr, "pics"); !got.Equal(flickrTS) {
		t.Errorf("flickr \"pics\": want %v, got %v", flickrTS, got)
	}
}

// ── RemoveSourceData ──────────────────────────────────────────────────────

func TestStorage_RemoveSourceData(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts(testListID, []Post{
		makePost("a", "pics", 0),
		makePost("b", "pics", 0),
		makePost("c", "videos", 0),
	})
	s.SetLastChecked(testListID, SourceReddit, "pics", time.Now().UTC())

	if err := s.RemoveSourceData(testListID, SourceReddit, "pics"); err != nil {
		t.Fatalf("RemoveSourceData: %v", err)
	}

	posts := s.GetPosts(testListID)
	if len(posts) != 1 || posts[0].ID != "c" {
		t.Errorf("want only post c remaining, got %v", posts)
	}
	if !s.GetLastChecked(testListID, SourceReddit, "pics").IsZero() {
		t.Error("last_checked for removed subreddit should be zero")
	}
}

func TestStorage_RemoveSourceData_PreservesFavorites(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts(testListID, []Post{
		makePost("fav", "pics", 0),
		makePost("nonfav", "pics", 0),
	})
	s.SetFavorite(testListID, "fav", true)

	s.RemoveSourceData(testListID, SourceReddit, "pics")

	posts := s.GetPosts(testListID)
	if len(posts) != 1 || posts[0].ID != "fav" {
		t.Errorf("favorited post should survive removal, got %v", posts)
	}
}

func TestStorage_RemoveSourceData_DoesNotCollideAcrossSources(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts(testListID, []Post{
		makePostSource("r1", SourceReddit, "pics", 0),
		makePostSource("f1", SourceFlickr, "pics", 0),
	})
	s.SetLastChecked(testListID, SourceReddit, "pics", time.Now().UTC())
	s.SetLastChecked(testListID, SourceFlickr, "pics", time.Now().UTC())

	if err := s.RemoveSourceData(testListID, SourceFlickr, "pics"); err != nil {
		t.Fatalf("RemoveSourceData: %v", err)
	}

	posts := s.GetPosts(testListID)
	if len(posts) != 1 || posts[0].ID != "r1" {
		t.Errorf("reddit post named \"pics\" should survive removing the flickr \"pics\" source, got %v", posts)
	}
	if s.GetLastChecked(testListID, SourceReddit, "pics").IsZero() {
		t.Error("reddit \"pics\" last-checked should survive removing the flickr \"pics\" source")
	}
	if !s.GetLastChecked(testListID, SourceFlickr, "pics").IsZero() {
		t.Error("flickr \"pics\" last-checked should be cleared")
	}
}

// ── PruneOldPosts ─────────────────────────────────────────────────────────

func TestStorage_PruneOldPosts(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts(testListID, []Post{
		makePost("old", "pics", 40*24*time.Hour), // 40 days old
		makePost("new", "pics", 5*24*time.Hour),  // 5 days old
		makePost("fav", "pics", 40*24*time.Hour), // 40 days old but favorited
	})
	s.SetFavorite(testListID, "fav", true)

	if err := s.PruneOldPosts(testListID, 30); err != nil {
		t.Fatalf("PruneOldPosts: %v", err)
	}

	ids := map[string]bool{}
	for _, p := range s.GetPosts(testListID) {
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
	s.AddPosts(testListID, []Post{makePost("ancient", "pics", 365*24*time.Hour)})

	if err := s.PruneOldPosts(testListID, 0); err != nil {
		t.Fatalf("PruneOldPosts(0): %v", err)
	}
	if len(s.GetPosts(testListID)) != 1 {
		t.Error("maxAgeDays=0 should disable pruning")
	}
}

// ── Multi-list isolation ──────────────────────────────────────────────────

func TestStorage_ListIsolation(t *testing.T) {
	s := newTestStorage(t)
	p := makePost("shared-id", "pics", 0)

	// Same post ID and same primary media URL, added independently to two lists.
	s.AddPosts("list1", []Post{p})
	s.AddPosts("list2", []Post{p})

	if got := len(s.GetPosts("list1")); got != 1 {
		t.Errorf("list1: want 1 post, got %d", got)
	}
	if got := len(s.GetPosts("list2")); got != 1 {
		t.Errorf("list2: want 1 post, got %d", got)
	}

	// Favoriting in one list must not affect the other.
	s.SetFavorite("list1", "shared-id", true)
	if s.GetFavorites("list2")["shared-id"] {
		t.Error("favorite in list1 should not leak into list2")
	}
}

func TestStorage_RemoveList_DeletesFavoritesToo(t *testing.T) {
	s := newTestStorage(t)
	s.AddPosts(testListID, []Post{makePost("fav", "pics", 0)})
	s.SetFavorite(testListID, "fav", true)

	if err := s.RemoveList(testListID); err != nil {
		t.Fatalf("RemoveList: %v", err)
	}

	if got := len(s.GetPosts(testListID)); got != 0 {
		t.Errorf("want 0 posts after RemoveList, got %d", got)
	}
	if got := len(s.GetFavorites(testListID)); got != 0 {
		t.Errorf("want 0 favorites after RemoveList, got %d", got)
	}
}

func TestStorage_RemoveList_Idempotent(t *testing.T) {
	s := newTestStorage(t)
	if err := s.RemoveList("never-created"); err != nil {
		t.Errorf("RemoveList on nonexistent list should not error, got %v", err)
	}
}

func TestStorage_GetPosts_UnknownList_NoVivify(t *testing.T) {
	s := newTestStorage(t)
	_ = s.GetPosts("nonexistent")
	_ = s.GetFavorites("nonexistent")
	_ = s.GetLastChecked("nonexistent", SourceReddit, "pics")

	s2, err := NewStorage(s.filePath)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := s2.data.Lists["nonexistent"]; ok {
		t.Error("read-only Storage methods should not persist a bucket for an unknown list ID")
	}
}

func TestStorage_AddPosts_UnknownList_CreatesIt(t *testing.T) {
	s := newTestStorage(t)
	if err := s.AddPosts("brand-new", []Post{makePost("a", "pics", 0)}); err != nil {
		t.Fatalf("AddPosts: %v", err)
	}
	if got := len(s.GetPosts("brand-new")); got != 1 {
		t.Errorf("want 1 post in newly-created list, got %d", got)
	}
}
