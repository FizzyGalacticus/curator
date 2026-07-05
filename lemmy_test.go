package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newLemmyTestClient creates a LemmyClient whose requests all go to srv,
// regardless of the instance parsed out of the identifier.
func newLemmyTestClient(srv *httptest.Server) *LemmyClient {
	return &LemmyClient{
		http:    &http.Client{},
		baseURL: srv.URL,
	}
}

func TestLemmyFetchNewPosts_ImagePost(t *testing.T) {
	resp := lemmyPostListResponse{
		Posts: []lemmyPostView{
			{
				Post: lemmyPost{
					ID:           123,
					Name:         "A cool pic",
					URL:          "https://example.com/pic.jpg",
					ThumbnailURL: "https://example.com/pic_thumb.jpg",
					Published:    time.Now().UTC().Format(time.RFC3339Nano),
					APID:         "https://lemmy.world/post/123",
				},
				Community: lemmyCommunity{Name: "pics"},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newLemmyTestClient(srv)
	posts, err := client.FetchNewPosts("pics@lemmy.world", time.Time{}, FetchCredentials{})
	if err != nil {
		t.Fatalf("FetchNewPosts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("want 1 post, got %d", len(posts))
	}

	p := posts[0]
	if p.ID != "lemmy_lemmy.world_123" {
		t.Errorf("ID = %q, want lemmy_lemmy.world_123", p.ID)
	}
	if p.Source != SourceLemmy {
		t.Errorf("Source = %q, want %q", p.Source, SourceLemmy)
	}
	if p.Subreddit != "pics@lemmy.world" {
		t.Errorf("Subreddit = %q, want pics@lemmy.world", p.Subreddit)
	}
	if p.Permalink != "https://lemmy.world/post/123" {
		t.Errorf("Permalink = %q, want ap_id", p.Permalink)
	}
	if len(p.MediaItems) != 1 || p.MediaItems[0].URL != "https://example.com/pic.jpg" {
		t.Fatalf("unexpected media items: %v", p.MediaItems)
	}
	if p.MediaItems[0].Type != MediaImage {
		t.Errorf("Type = %q, want image", p.MediaItems[0].Type)
	}
}

func TestLemmyFetchNewPosts_SkipsNonMediaAndNSFW(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	resp := lemmyPostListResponse{
		Posts: []lemmyPostView{
			{Post: lemmyPost{ID: 1, Published: now}, Community: lemmyCommunity{Name: "pics"}},                                                 // no URL
			{Post: lemmyPost{ID: 2, URL: "https://example.com/article", Published: now}, Community: lemmyCommunity{Name: "pics"}},             // not media
			{Post: lemmyPost{ID: 3, URL: "https://example.com/pic.jpg", Published: now, NSFW: true}, Community: lemmyCommunity{Name: "pics"}}, // post nsfw
			{Post: lemmyPost{ID: 4, URL: "https://example.com/pic.jpg", Published: now}, Community: lemmyCommunity{Name: "pics", NSFW: true}}, // community nsfw
			{Post: lemmyPost{ID: 5, URL: "https://example.com/good.jpg", Published: now}, Community: lemmyCommunity{Name: "pics"}},            // kept
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newLemmyTestClient(srv)
	posts, err := client.FetchNewPosts("pics@lemmy.world", time.Time{}, FetchCredentials{})
	if err != nil {
		t.Fatalf("FetchNewPosts: %v", err)
	}
	if len(posts) != 1 || posts[0].ID != "lemmy_lemmy.world_5" {
		t.Errorf("want only post 5 kept, got %v", posts)
	}
}

func TestLemmyFetchNewPosts_SinceFilter(t *testing.T) {
	now := time.Now().UTC()
	// Simulates a featured/pinned post (old) appearing before a newer post,
	// which is real observed Lemmy API behavior even with sort=New.
	resp := lemmyPostListResponse{
		Posts: []lemmyPostView{
			{
				Post: lemmyPost{
					ID: 1, URL: "https://example.com/old.jpg",
					Published: now.Add(-5 * time.Hour).Format(time.RFC3339Nano),
				},
				Community: lemmyCommunity{Name: "pics"},
			},
			{
				Post: lemmyPost{
					ID: 2, URL: "https://example.com/new.jpg",
					Published: now.Add(-1 * time.Hour).Format(time.RFC3339Nano),
				},
				Community: lemmyCommunity{Name: "pics"},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	client := newLemmyTestClient(srv)
	since := now.Add(-3 * time.Hour)
	posts, err := client.FetchNewPosts("pics@lemmy.world", since, FetchCredentials{})
	if err != nil {
		t.Fatalf("FetchNewPosts: %v", err)
	}
	if len(posts) != 1 || posts[0].ID != "lemmy_lemmy.world_2" {
		t.Errorf("since filter: want only the newer post, got %v", posts)
	}
}

func TestLemmyFetchNewPosts_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newLemmyTestClient(srv)
	_, err := client.FetchNewPosts("pics@lemmy.world", time.Time{}, FetchCredentials{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLemmyFetchNewPosts_InvalidIdentifier(t *testing.T) {
	client := &LemmyClient{http: &http.Client{}}
	_, err := client.FetchNewPosts("noatsign", time.Time{}, FetchCredentials{})
	if err == nil {
		t.Fatal("expected error for invalid identifier, got nil")
	}
}

func TestSplitLemmyIdentifier(t *testing.T) {
	tests := []struct {
		input         string
		wantCommunity string
		wantInstance  string
		wantOK        bool
	}{
		{"pics@lemmy.world", "pics", "lemmy.world", true},
		{"!pics@lemmy.world", "pics", "lemmy.world", true},
		{"noatsign", "", "", false},
		{"@nocommunity", "", "", false},
		{"comm@", "", "", false},
	}
	for _, tc := range tests {
		community, instance, ok := splitLemmyIdentifier(tc.input)
		if ok != tc.wantOK {
			t.Errorf("splitLemmyIdentifier(%q): ok = %v, want %v", tc.input, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if community != tc.wantCommunity || instance != tc.wantInstance {
			t.Errorf("splitLemmyIdentifier(%q) = (%q, %q), want (%q, %q)", tc.input, community, instance, tc.wantCommunity, tc.wantInstance)
		}
	}
}
