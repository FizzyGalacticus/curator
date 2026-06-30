package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

// newTestAPIServer builds an APIServer with temp storage and config for handler tests.
func newTestAPIServer(t *testing.T) (*APIServer, chan struct{}) {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	storage, err := NewStorage(filepath.Join(dir, "data.json"))
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	ch := make(chan struct{}, 1)
	return &APIServer{
		config:     cfg,
		storage:    storage,
		configPath: filepath.Join(dir, "config.json"),
		refreshCh:  ch,
	}, ch
}

// decodeAPIResponse decodes a JSON response into apiResponse and returns the
// raw Data field for further unmarshalling by the caller.
func decodeAPIResponse(t *testing.T, rr *httptest.ResponseRecorder) (bool, string, json.RawMessage) {
	t.Helper()
	var resp struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.Success, resp.Message, resp.Data
}

func makeTestPost(id, subreddit string, createdAgo time.Duration) Post {
	return Post{
		ID:         id,
		Subreddit:  subreddit,
		Title:      "Title " + id,
		CreatedAt:  time.Now().UTC().Add(-createdAgo),
		MediaItems: []MediaItem{{Type: MediaImage, URL: "https://example.com/" + id + ".jpg"}},
	}
}

// ── GET /api/posts ────────────────────────────────────────────────────────

func TestHandlePosts_Empty(t *testing.T) {
	api, _ := newTestAPIServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/posts", nil)
	rr := httptest.NewRecorder()
	api.handlePosts(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	ok, _, data := decodeAPIResponse(t, rr)
	if !ok {
		t.Fatal("want success=true")
	}
	var posts []PostWithFavorite
	json.Unmarshal(data, &posts)
	if len(posts) != 0 {
		t.Errorf("want empty list, got %d posts", len(posts))
	}
}

func TestHandlePosts_FavoritesFirst(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.storage.AddPosts([]Post{
		makeTestPost("older", "pics", 2*time.Hour),
		makeTestPost("newer", "pics", 1*time.Hour),
		makeTestPost("fav", "pics", 3*time.Hour),
	})
	api.storage.SetFavorite("fav", true)

	req := httptest.NewRequest(http.MethodGet, "/api/posts", nil)
	rr := httptest.NewRecorder()
	api.handlePosts(rr, req)

	_, _, data := decodeAPIResponse(t, rr)
	var posts []PostWithFavorite
	json.Unmarshal(data, &posts)

	if len(posts) != 3 {
		t.Fatalf("want 3 posts, got %d", len(posts))
	}
	if posts[0].ID != "fav" || !posts[0].Favorited {
		t.Errorf("first post should be favorited 'fav', got %s (fav=%v)", posts[0].ID, posts[0].Favorited)
	}
	// Remaining should be newest first.
	if posts[1].ID != "newer" {
		t.Errorf("second post should be 'newer', got %s", posts[1].ID)
	}
}

func TestHandlePosts_FilterFavorites(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.storage.AddPosts([]Post{makeTestPost("a", "pics", 0), makeTestPost("b", "pics", 0)})
	api.storage.SetFavorite("a", true)

	req := httptest.NewRequest(http.MethodGet, "/api/posts?filter=favorites", nil)
	rr := httptest.NewRecorder()
	api.handlePosts(rr, req)

	_, _, data := decodeAPIResponse(t, rr)
	var posts []PostWithFavorite
	json.Unmarshal(data, &posts)
	if len(posts) != 1 || posts[0].ID != "a" {
		t.Errorf("filter=favorites: want only 'a', got %v", posts)
	}
}

func TestHandlePosts_FilterNonFavorites(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.storage.AddPosts([]Post{makeTestPost("a", "pics", 0), makeTestPost("b", "pics", 0)})
	api.storage.SetFavorite("a", true)

	req := httptest.NewRequest(http.MethodGet, "/api/posts?filter=non-favorites", nil)
	rr := httptest.NewRecorder()
	api.handlePosts(rr, req)

	_, _, data := decodeAPIResponse(t, rr)
	var posts []PostWithFavorite
	json.Unmarshal(data, &posts)
	if len(posts) != 1 || posts[0].ID != "b" {
		t.Errorf("filter=non-favorites: want only 'b', got %v", posts)
	}
}

func TestHandlePosts_SubredditFilter(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.storage.AddPosts([]Post{
		makeTestPost("p1", "pics", 0),
		makeTestPost("v1", "videos", 0),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/posts?subreddit=pics", nil)
	rr := httptest.NewRecorder()
	api.handlePosts(rr, req)

	_, _, data := decodeAPIResponse(t, rr)
	var posts []PostWithFavorite
	json.Unmarshal(data, &posts)
	if len(posts) != 1 || posts[0].ID != "p1" {
		t.Errorf("subreddit filter: want [p1], got %v", posts)
	}
}

func TestHandlePosts_MethodNotAllowed(t *testing.T) {
	api, _ := newTestAPIServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/posts", nil)
	rr := httptest.NewRecorder()
	api.handlePosts(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rr.Code)
	}
}

// ── POST /api/posts/{id}/favorite ─────────────────────────────────────────

func TestHandleToggleFavorite(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.storage.AddPosts([]Post{makeTestPost("xyz", "pics", 0)})

	// Set download dir to empty so DownloadMedia is a no-op.
	api.config.DownloadDir = ""

	req := httptest.NewRequest(http.MethodPost, "/api/posts/xyz/favorite", nil)
	rr := httptest.NewRecorder()
	api.handlePostByID(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	ok, _, data := decodeAPIResponse(t, rr)
	if !ok {
		t.Fatal("want success=true")
	}
	var result map[string]bool
	json.Unmarshal(data, &result)
	if !result["favorited"] {
		t.Error("want favorited=true on first toggle")
	}

	// Toggle again → false.
	req2 := httptest.NewRequest(http.MethodPost, "/api/posts/xyz/favorite", nil)
	rr2 := httptest.NewRecorder()
	api.handlePostByID(rr2, req2)
	_, _, data2 := decodeAPIResponse(t, rr2)
	json.Unmarshal(data2, &result)
	if result["favorited"] {
		t.Error("want favorited=false on second toggle")
	}
}

// ── GET /api/config ────────────────────────────────────────────────────────

func TestHandleConfig_Get(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.config.CheckInterval = "15m"
	api.config.ImgurClientID = "clientabc"

	req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
	rr := httptest.NewRecorder()
	api.handleConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	_, _, data := decodeAPIResponse(t, rr)
	var cfg map[string]interface{}
	json.Unmarshal(data, &cfg)

	if cfg["check_interval"] != "15m" {
		t.Errorf("check_interval: want 15m, got %v", cfg["check_interval"])
	}
	if cfg["imgur_client_id"] != "clientabc" {
		t.Errorf("imgur_client_id: want clientabc, got %v", cfg["imgur_client_id"])
	}
}

// ── PUT /api/config ────────────────────────────────────────────────────────

func TestHandleConfig_Put(t *testing.T) {
	api, _ := newTestAPIServer(t)

	body, _ := json.Marshal(map[string]interface{}{
		"check_interval":    "2h",
		"max_post_age_days": float64(14),
		"imgur_client_id":   "newclient",
	})
	req := httptest.NewRequest(http.MethodPut, "/api/config", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	api.handleConfig(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d; body: %s", rr.Code, rr.Body.String())
	}

	api.config.RLock()
	defer api.config.RUnlock()
	if api.config.CheckInterval != "2h" {
		t.Errorf("CheckInterval: want 2h, got %s", api.config.CheckInterval)
	}
	if api.config.MaxPostAgeDays != 14 {
		t.Errorf("MaxPostAgeDays: want 14, got %d", api.config.MaxPostAgeDays)
	}
	if api.config.ImgurClientID != "newclient" {
		t.Errorf("ImgurClientID: want newclient, got %s", api.config.ImgurClientID)
	}
}

// ── GET + POST /api/subreddits ─────────────────────────────────────────────

func TestHandleSubreddits_GetAndAdd(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.config.Subreddits = []string{"pics"}

	// GET returns current list.
	req := httptest.NewRequest(http.MethodGet, "/api/subreddits", nil)
	rr := httptest.NewRecorder()
	api.handleSubreddits(rr, req)
	_, _, data := decodeAPIResponse(t, rr)
	var subs []string
	json.Unmarshal(data, &subs)
	if len(subs) != 1 || subs[0] != "pics" {
		t.Errorf("GET subreddits: want [pics], got %v", subs)
	}

	// POST adds a new one.
	body, _ := json.Marshal(map[string]string{"name": "videos"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/subreddits", bytes.NewReader(body))
	rr2 := httptest.NewRecorder()
	api.handleSubreddits(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("POST subreddit: want 200, got %d", rr2.Code)
	}

	if !contains(api.config.GetSubreddits(), "videos") {
		t.Error("videos should be in subreddits after POST")
	}
}

func TestHandleSubreddits_AddEmpty(t *testing.T) {
	api, _ := newTestAPIServer(t)
	body, _ := json.Marshal(map[string]string{"name": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/subreddits", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	api.handleSubreddits(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty name, got %d", rr.Code)
	}
}

// ── DELETE /api/subreddits/{name} ─────────────────────────────────────────

func TestHandleSubredditByName_Delete(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.config.Subreddits = []string{"pics", "videos"}
	api.storage.AddPosts([]Post{makeTestPost("p1", "pics", 0)})

	req := httptest.NewRequest(http.MethodDelete, "/api/subreddits/pics", nil)
	rr := httptest.NewRecorder()
	api.handleSubredditByName(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	if contains(api.config.GetSubreddits(), "pics") {
		t.Error("pics should be removed from config")
	}
	// Posts from deleted subreddit should be gone (unless favorited).
	for _, p := range api.storage.GetPosts() {
		if p.Subreddit == "pics" {
			t.Error("non-favorited posts from deleted subreddit should be removed")
		}
	}
}

// ── POST /api/refresh ─────────────────────────────────────────────────────

func TestHandleRefresh(t *testing.T) {
	api, ch := newTestAPIServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rr := httptest.NewRecorder()
	api.handleRefresh(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	select {
	case <-ch:
		// expected
	default:
		t.Error("handleRefresh should send a value to refreshCh")
	}
}

func TestHandleRefresh_MethodNotAllowed(t *testing.T) {
	api, _ := newTestAPIServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/refresh", nil)
	rr := httptest.NewRecorder()
	api.handleRefresh(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rr.Code)
	}
}

// ── GET /api/status ───────────────────────────────────────────────────────

func TestHandleStatus(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.config.Subreddits = []string{"pics"}
	api.storage.AddPosts([]Post{makeTestPost("p1", "pics", 0)})
	api.storage.SetFavorite("p1", true)

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rr := httptest.NewRecorder()
	api.handleStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	_, _, data := decodeAPIResponse(t, rr)
	var status map[string]interface{}
	json.Unmarshal(data, &status)

	if status["posts_count"].(float64) != 1 {
		t.Errorf("posts_count: want 1, got %v", status["posts_count"])
	}
	if status["favorites_count"].(float64) != 1 {
		t.Errorf("favorites_count: want 1, got %v", status["favorites_count"])
	}
}

// ── helper ────────────────────────────────────────────────────────────────

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
