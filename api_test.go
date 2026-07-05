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

// addTestList creates a curation list directly against the config, mirroring
// how tests set up state without going through HTTP.
func addTestList(t *testing.T, api *APIServer, name string, subs []string) CurationList {
	t.Helper()
	l, err := api.config.AddList(NewListInput{Name: name, Subreddits: subs})
	if err != nil {
		t.Fatalf("AddList: %v", err)
	}
	return l
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
		Source:     SourceReddit,
		Subreddit:  subreddit,
		Title:      "Title " + id,
		CreatedAt:  time.Now().UTC().Add(-createdAgo),
		MediaItems: []MediaItem{{Type: MediaImage, URL: "https://example.com/" + id + ".jpg"}},
	}
}

// ── GET /api/lists/{id}/posts ─────────────────────────────────────────────

func TestHandleListPosts_Empty(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)

	req := httptest.NewRequest(http.MethodGet, "/api/lists/"+list.ID+"/posts", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListPosts(rr, req)

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

func TestHandleListPosts_UnknownList_404(t *testing.T) {
	api, _ := newTestAPIServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/lists/nope/posts", nil)
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	api.handleListPosts(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestHandleListPosts_FavoritesFirst(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	api.storage.AddPosts(list.ID, []Post{
		makeTestPost("older", "pics", 2*time.Hour),
		makeTestPost("newer", "pics", 1*time.Hour),
		makeTestPost("fav", "pics", 3*time.Hour),
	})
	api.storage.SetFavorite(list.ID, "fav", true)

	req := httptest.NewRequest(http.MethodGet, "/api/lists/"+list.ID+"/posts", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListPosts(rr, req)

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

func TestHandleListPosts_FilterFavorites(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	api.storage.AddPosts(list.ID, []Post{makeTestPost("a", "pics", 0), makeTestPost("b", "pics", 0)})
	api.storage.SetFavorite(list.ID, "a", true)

	req := httptest.NewRequest(http.MethodGet, "/api/lists/"+list.ID+"/posts?filter=favorites", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListPosts(rr, req)

	_, _, data := decodeAPIResponse(t, rr)
	var posts []PostWithFavorite
	json.Unmarshal(data, &posts)
	if len(posts) != 1 || posts[0].ID != "a" {
		t.Errorf("filter=favorites: want only 'a', got %v", posts)
	}
}

func TestHandleListPosts_FilterNonFavorites(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	api.storage.AddPosts(list.ID, []Post{makeTestPost("a", "pics", 0), makeTestPost("b", "pics", 0)})
	api.storage.SetFavorite(list.ID, "a", true)

	req := httptest.NewRequest(http.MethodGet, "/api/lists/"+list.ID+"/posts?filter=non-favorites", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListPosts(rr, req)

	_, _, data := decodeAPIResponse(t, rr)
	var posts []PostWithFavorite
	json.Unmarshal(data, &posts)
	if len(posts) != 1 || posts[0].ID != "b" {
		t.Errorf("filter=non-favorites: want only 'b', got %v", posts)
	}
}

func TestHandleListPosts_SubredditFilter(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	api.storage.AddPosts(list.ID, []Post{
		makeTestPost("p1", "pics", 0),
		makeTestPost("v1", "videos", 0),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/lists/"+list.ID+"/posts?subreddit=pics", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListPosts(rr, req)

	_, _, data := decodeAPIResponse(t, rr)
	var posts []PostWithFavorite
	json.Unmarshal(data, &posts)
	if len(posts) != 1 || posts[0].ID != "p1" {
		t.Errorf("subreddit filter: want [p1], got %v", posts)
	}
}

func TestHandleListPosts_MethodNotAllowed(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/posts", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListPosts(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rr.Code)
	}
}

// ── POST /api/lists/{id}/posts/{postId}/favorite ──────────────────────────

func TestHandleListPostFavorite(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	api.storage.AddPosts(list.ID, []Post{makeTestPost("xyz", "pics", 0)})

	// Set download dir to empty so DownloadMedia is a no-op.
	api.config.DownloadDir = ""

	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/posts/xyz/favorite", nil)
	req.SetPathValue("id", list.ID)
	req.SetPathValue("postId", "xyz")
	rr := httptest.NewRecorder()
	api.handleListPostFavorite(rr, req)

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
	req2 := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/posts/xyz/favorite", nil)
	req2.SetPathValue("id", list.ID)
	req2.SetPathValue("postId", "xyz")
	rr2 := httptest.NewRecorder()
	api.handleListPostFavorite(rr2, req2)
	_, _, data2 := decodeAPIResponse(t, rr2)
	json.Unmarshal(data2, &result)
	if result["favorited"] {
		t.Error("want favorited=false on second toggle")
	}
}

func TestHandleListPostFavorite_UnknownList_404(t *testing.T) {
	api, _ := newTestAPIServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/lists/nope/posts/xyz/favorite", nil)
	req.SetPathValue("id", "nope")
	req.SetPathValue("postId", "xyz")
	rr := httptest.NewRecorder()
	api.handleListPostFavorite(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

// ── GET /api/config ────────────────────────────────────────────────────────

func TestHandleConfig_Get(t *testing.T) {
	api, _ := newTestAPIServer(t)
	api.config.CheckInterval = "15m"
	api.config.ImgurClientID = "clientabc"
	api.config.FlickrAPIKey = "flickrkey123"

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
	if cfg["flickr_api_key"] != "flickrkey123" {
		t.Errorf("flickr_api_key: want flickrkey123, got %v", cfg["flickr_api_key"])
	}
	if _, ok := cfg["subreddits"]; ok {
		t.Error("global config should no longer include a subreddits field")
	}
}

// ── PUT /api/config ────────────────────────────────────────────────────────

func TestHandleConfig_Put(t *testing.T) {
	api, _ := newTestAPIServer(t)

	body, _ := json.Marshal(map[string]interface{}{
		"check_interval":    "2h",
		"max_post_age_days": float64(14),
		"imgur_client_id":   "newclient",
		"flickr_api_key":    "newflickrkey",
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
	if api.config.FlickrAPIKey != "newflickrkey" {
		t.Errorf("FlickrAPIKey: want newflickrkey, got %s", api.config.FlickrAPIKey)
	}
}

// ── GET/POST/PUT/DELETE /api/lists ─────────────────────────────────────────

func TestHandleLists_CRUD(t *testing.T) {
	api, _ := newTestAPIServer(t)

	// Create.
	body, _ := json.Marshal(map[string]interface{}{
		"name":              "SFW",
		"subreddits":        []string{"pics", "wallpapers"},
		"flickr_groups":     []string{"blackandwhite"},
		"lemmy_communities": []string{"pics@lemmy.world"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/lists", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	api.handleListsIndex(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create: want 200, got %d; body: %s", rr.Code, rr.Body.String())
	}
	_, _, data := decodeAPIResponse(t, rr)
	var created CurationList
	json.Unmarshal(data, &created)
	if created.ID == "" || created.Name != "SFW" || len(created.Subreddits) != 2 {
		t.Fatalf("unexpected created list: %+v", created)
	}
	if len(created.FlickrGroups) != 1 || created.FlickrGroups[0] != "blackandwhite" {
		t.Errorf("FlickrGroups: want [blackandwhite], got %v", created.FlickrGroups)
	}
	if len(created.LemmyCommunities) != 1 || created.LemmyCommunities[0] != "pics@lemmy.world" {
		t.Errorf("LemmyCommunities: want [pics@lemmy.world], got %v", created.LemmyCommunities)
	}

	// Add a post so counts show up.
	api.storage.AddPosts(created.ID, []Post{makeTestPost("p1", "pics", 0)})
	api.storage.SetFavorite(created.ID, "p1", true)

	// GET /api/lists shows it with correct counts.
	req2 := httptest.NewRequest(http.MethodGet, "/api/lists", nil)
	rr2 := httptest.NewRecorder()
	api.handleListsIndex(rr2, req2)
	_, _, data2 := decodeAPIResponse(t, rr2)
	var summaries []map[string]interface{}
	json.Unmarshal(data2, &summaries)
	if len(summaries) != 1 {
		t.Fatalf("want 1 list summary, got %d", len(summaries))
	}
	if summaries[0]["post_count"].(float64) != 1 {
		t.Errorf("post_count: want 1, got %v", summaries[0]["post_count"])
	}
	if summaries[0]["favorite_count"].(float64) != 1 {
		t.Errorf("favorite_count: want 1, got %v", summaries[0]["favorite_count"])
	}
	if fg, ok := summaries[0]["flickr_groups"].([]interface{}); !ok || len(fg) != 1 {
		t.Errorf("flickr_groups: want [blackandwhite], got %v", summaries[0]["flickr_groups"])
	}
	if lc, ok := summaries[0]["lemmy_communities"].([]interface{}); !ok || len(lc) != 1 {
		t.Errorf("lemmy_communities: want [pics@lemmy.world], got %v", summaries[0]["lemmy_communities"])
	}

	// Rename.
	renameBody, _ := json.Marshal(map[string]string{"name": "Renamed"})
	req3 := httptest.NewRequest(http.MethodPut, "/api/lists/"+created.ID, bytes.NewReader(renameBody))
	req3.SetPathValue("id", created.ID)
	rr3 := httptest.NewRecorder()
	api.handleListByID(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("rename: want 200, got %d", rr3.Code)
	}
	updated, _ := api.config.GetList(created.ID)
	if updated.Name != "Renamed" {
		t.Errorf("Name: want Renamed, got %s", updated.Name)
	}

	// Delete.
	req4 := httptest.NewRequest(http.MethodDelete, "/api/lists/"+created.ID, nil)
	req4.SetPathValue("id", created.ID)
	rr4 := httptest.NewRecorder()
	api.handleListByID(rr4, req4)
	if rr4.Code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d", rr4.Code)
	}
	if _, ok := api.config.GetList(created.ID); ok {
		t.Error("list should be gone from config after delete")
	}
	if got := len(api.storage.GetPosts(created.ID)); got != 0 {
		t.Errorf("storage data should be gone after delete, got %d posts", got)
	}
}

func TestHandleListByID_UnknownList_404(t *testing.T) {
	api, _ := newTestAPIServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/lists/nope", nil)
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	api.handleListByID(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

// ── POST /api/lists/{id}/subreddits, DELETE /api/lists/{id}/subreddits/{name} ─

func TestHandleListSubreddits_Add(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", []string{"pics"})

	body, _ := json.Marshal(map[string]string{"name": "videos"})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/subreddits", bytes.NewReader(body))
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListSubreddits(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST subreddit: want 200, got %d", rr.Code)
	}

	updated, _ := api.config.GetList(list.ID)
	if !contains(updated.Subreddits, "videos") {
		t.Error("videos should be in list's subreddits after POST")
	}
}

func TestHandleListSubreddits_AddEmpty(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	body, _ := json.Marshal(map[string]string{"name": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/subreddits", bytes.NewReader(body))
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListSubreddits(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty name, got %d", rr.Code)
	}
}

func TestHandleListSubreddits_UnknownList_404(t *testing.T) {
	api, _ := newTestAPIServer(t)
	body, _ := json.Marshal(map[string]string{"name": "pics"})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/nope/subreddits", bytes.NewReader(body))
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	api.handleListSubreddits(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestHandleListSubredditByName_Delete(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", []string{"pics", "videos"})
	api.storage.AddPosts(list.ID, []Post{makeTestPost("p1", "pics", 0)})

	req := httptest.NewRequest(http.MethodDelete, "/api/lists/"+list.ID+"/subreddits/pics", nil)
	req.SetPathValue("id", list.ID)
	req.SetPathValue("name", "pics")
	rr := httptest.NewRecorder()
	api.handleListSubredditByName(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	updated, _ := api.config.GetList(list.ID)
	if contains(updated.Subreddits, "pics") {
		t.Error("pics should be removed from list's subreddits")
	}
	// Posts from deleted subreddit should be gone (unless favorited).
	for _, p := range api.storage.GetPosts(list.ID) {
		if p.Subreddit == "pics" {
			t.Error("non-favorited posts from deleted subreddit should be removed")
		}
	}
}

// ── POST /api/lists/{id}/flickr-groups, DELETE .../flickr-groups/{name} ────

func TestHandleListFlickrGroups_Add(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)

	body, _ := json.Marshal(map[string]string{"name": "blackandwhite"})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/flickr-groups", bytes.NewReader(body))
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListFlickrGroups(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST flickr group: want 200, got %d", rr.Code)
	}

	updated, _ := api.config.GetList(list.ID)
	if !contains(updated.FlickrGroups, "blackandwhite") {
		t.Error("blackandwhite should be in list's flickr groups after POST")
	}
}

func TestHandleListFlickrGroups_AddEmpty(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	body, _ := json.Marshal(map[string]string{"name": ""})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/flickr-groups", bytes.NewReader(body))
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListFlickrGroups(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for empty name, got %d", rr.Code)
	}
}

func TestHandleListFlickrGroups_UnknownList_404(t *testing.T) {
	api, _ := newTestAPIServer(t)
	body, _ := json.Marshal(map[string]string{"name": "blackandwhite"})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/nope/flickr-groups", bytes.NewReader(body))
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	api.handleListFlickrGroups(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestHandleListFlickrGroupByName_Delete(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	api.config.AddFlickrGroupToList(list.ID, "blackandwhite")
	api.storage.AddPosts(list.ID, []Post{makePostSource("f1", SourceFlickr, "blackandwhite", 0)})

	req := httptest.NewRequest(http.MethodDelete, "/api/lists/"+list.ID+"/flickr-groups/blackandwhite", nil)
	req.SetPathValue("id", list.ID)
	req.SetPathValue("name", "blackandwhite")
	rr := httptest.NewRecorder()
	api.handleListFlickrGroupByName(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	updated, _ := api.config.GetList(list.ID)
	if contains(updated.FlickrGroups, "blackandwhite") {
		t.Error("blackandwhite should be removed from list's flickr groups")
	}
	if got := len(api.storage.GetPosts(list.ID)); got != 0 {
		t.Errorf("posts from deleted flickr group should be removed, got %d", got)
	}
}

// ── POST /api/lists/{id}/lemmy-communities, DELETE .../lemmy-communities/{name} ─

func TestHandleListLemmyCommunities_Add(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)

	body, _ := json.Marshal(map[string]string{"name": "pics@lemmy.world"})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/lemmy-communities", bytes.NewReader(body))
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListLemmyCommunities(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST lemmy community: want 200, got %d", rr.Code)
	}

	updated, _ := api.config.GetList(list.ID)
	if !contains(updated.LemmyCommunities, "pics@lemmy.world") {
		t.Error("pics@lemmy.world should be in list's lemmy communities after POST")
	}
}

func TestHandleListLemmyCommunities_AddInvalid(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	body, _ := json.Marshal(map[string]string{"name": "noatsign"})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/lemmy-communities", bytes.NewReader(body))
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListLemmyCommunities(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400 for invalid identifier, got %d", rr.Code)
	}
}

func TestHandleListLemmyCommunities_UnknownList_404(t *testing.T) {
	api, _ := newTestAPIServer(t)
	body, _ := json.Marshal(map[string]string{"name": "pics@lemmy.world"})
	req := httptest.NewRequest(http.MethodPost, "/api/lists/nope/lemmy-communities", bytes.NewReader(body))
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	api.handleListLemmyCommunities(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestHandleListLemmyCommunityByName_Delete(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	api.config.AddLemmyCommunityToList(list.ID, "pics@lemmy.world")
	api.storage.AddPosts(list.ID, []Post{makePostSource("l1", SourceLemmy, "pics@lemmy.world", 0)})

	req := httptest.NewRequest(http.MethodDelete, "/api/lists/"+list.ID+"/lemmy-communities/pics@lemmy.world", nil)
	req.SetPathValue("id", list.ID)
	req.SetPathValue("name", "pics@lemmy.world")
	rr := httptest.NewRecorder()
	api.handleListLemmyCommunityByName(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	updated, _ := api.config.GetList(list.ID)
	if contains(updated.LemmyCommunities, "pics@lemmy.world") {
		t.Error("pics@lemmy.world should be removed from list's lemmy communities")
	}
	if got := len(api.storage.GetPosts(list.ID)); got != 0 {
		t.Errorf("posts from deleted lemmy community should be removed, got %d", got)
	}
}

// ── POST /api/lists/{id}/refresh ───────────────────────────────────────────

func TestHandleListRefresh(t *testing.T) {
	api, ch := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)

	req := httptest.NewRequest(http.MethodPost, "/api/lists/"+list.ID+"/refresh", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListRefresh(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}

	select {
	case <-ch:
		// expected
	default:
		t.Error("handleListRefresh should send a value to refreshCh")
	}
}

func TestHandleListRefresh_UnknownList_404(t *testing.T) {
	api, _ := newTestAPIServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/lists/nope/refresh", nil)
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	api.handleListRefresh(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
	}
}

func TestHandleListRefresh_MethodNotAllowed(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", nil)
	req := httptest.NewRequest(http.MethodGet, "/api/lists/"+list.ID+"/refresh", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListRefresh(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", rr.Code)
	}
}

// ── GET /api/lists/{id}/status ──────────────────────────────────────────────

func TestHandleListStatus(t *testing.T) {
	api, _ := newTestAPIServer(t)
	list := addTestList(t, api, "L", []string{"pics"})
	api.config.AddFlickrGroupToList(list.ID, "blackandwhite")
	api.config.AddLemmyCommunityToList(list.ID, "pics@lemmy.world")
	api.storage.AddPosts(list.ID, []Post{makeTestPost("p1", "pics", 0)})
	api.storage.SetFavorite(list.ID, "p1", true)
	api.storage.SetLastChecked(list.ID, SourceReddit, "pics", time.Now().UTC())
	api.storage.SetLastChecked(list.ID, SourceFlickr, "blackandwhite", time.Now().UTC())

	req := httptest.NewRequest(http.MethodGet, "/api/lists/"+list.ID+"/status", nil)
	req.SetPathValue("id", list.ID)
	rr := httptest.NewRecorder()
	api.handleListStatus(rr, req)

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

	lastChecked, ok := status["last_checked"].(map[string]interface{})
	if !ok {
		t.Fatalf("last_checked: want nested object, got %v", status["last_checked"])
	}
	reddit, ok := lastChecked["reddit"].(map[string]interface{})
	if !ok || reddit["pics"] == "never" {
		t.Errorf("last_checked.reddit.pics: want a timestamp, got %v", lastChecked["reddit"])
	}
	flickr, ok := lastChecked["flickr"].(map[string]interface{})
	if !ok || flickr["blackandwhite"] == "never" {
		t.Errorf("last_checked.flickr.blackandwhite: want a timestamp, got %v", lastChecked["flickr"])
	}
	lemmy, ok := lastChecked["lemmy"].(map[string]interface{})
	if !ok || lemmy["pics@lemmy.world"] != "never" {
		t.Errorf("last_checked.lemmy[pics@lemmy.world]: want \"never\", got %v", lastChecked["lemmy"])
	}
}

func TestHandleListStatus_UnknownList_404(t *testing.T) {
	api, _ := newTestAPIServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/lists/nope/status", nil)
	req.SetPathValue("id", "nope")
	rr := httptest.NewRecorder()
	api.handleListStatus(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d", rr.Code)
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
