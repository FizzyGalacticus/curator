package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type mockFetcher struct {
	fn func(subreddit string, since time.Time, creds FetchCredentials) ([]Post, error)
}

func (m *mockFetcher) FetchNewPosts(subreddit string, since time.Time, creds FetchCredentials) ([]Post, error) {
	return m.fn(subreddit, since, creds)
}

func mockScrolllerResponse(items []scrolllerPost) map[string]any {
	return map[string]any{
		"data": map[string]any{
			"getSubreddit": map[string]any{
				"id":     1234,
				"url":    "/r/testsubreddit",
				"title":  "testsubreddit",
				"isNsfw": false,
				"children": map[string]any{
					"iterator": "999",
					"items":    items,
				},
			},
		},
	}
}

func newScrolllerTestClient(srv *httptest.Server) *ScrolllerClient {
	return &ScrolllerClient{
		http:    &http.Client{Timeout: 5 * time.Second},
		baseURL: srv.URL,
	}
}

func TestScrolllerFetchNewPosts_Image(t *testing.T) {
	posts := []scrolllerPost{
		{
			Typename: "SubredditPost",
			ID:       1,
			URL:      "/test-post-abc",
			Title:    "Test Image",
			MediaSources: []scrolllerMediaSource{
				{URL: "https://images.scrolller.com/thumb.webp", Width: 100, Height: 100},
				{URL: "https://images.scrolller.com/full.jpg", Width: 1920, Height: 1080},
			},
		},
	}

	var gotSortBy any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			gotSortBy = req.Variables["sortBy"]
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockScrolllerResponse(posts))
	}))
	defer srv.Close()

	client := newScrolllerTestClient(srv)
	result, err := client.FetchNewPosts("testsubreddit", time.Time{}, FetchCredentials{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSortBy != "HOT" {
		t.Errorf("sortBy = %v, want HOT", gotSortBy)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 post, got %d", len(result))
	}

	p := result[0]
	if p.ID != "scrolller_1" {
		t.Errorf("ID = %q, want scrolller_1", p.ID)
	}
	if p.Subreddit != "testsubreddit" {
		t.Errorf("Subreddit = %q, want testsubreddit", p.Subreddit)
	}
	if p.Source != SourceReddit {
		t.Errorf("Source = %q, want %q", p.Source, SourceReddit)
	}
	if len(p.MediaItems) != 1 {
		t.Fatalf("expected 1 media item, got %d", len(p.MediaItems))
	}

	m := p.MediaItems[0]
	if m.Type != MediaImage {
		t.Errorf("Type = %q, want image", m.Type)
	}
	if m.URL != "https://images.scrolller.com/full.jpg" {
		t.Errorf("URL = %q, want full.jpg", m.URL)
	}
	if m.Thumbnail != "https://images.scrolller.com/thumb.webp" {
		t.Errorf("Thumbnail = %q, want thumb.webp", m.Thumbnail)
	}
	if m.Width != 1920 || m.Height != 1080 {
		t.Errorf("dimensions = %dx%d, want 1920x1080", m.Width, m.Height)
	}
}

func TestScrolllerFetchNewPosts_Video(t *testing.T) {
	str := "https://www.redgifs.com/watch/SomeSlugs"
	posts := []scrolllerPost{
		{
			Typename: "SubredditPost",
			ID:       42,
			URL:      "/test-video",
			Title:    "Test Video",
			MediaSources: []scrolllerMediaSource{
				{URL: "https://octa.scrolller.com/Video-mobile.mp4", Width: 854, Height: 480},
				{URL: "https://octa.scrolller.com/Video.mp4", Width: 1920, Height: 1080},
				{URL: "https://proton.scrolller.com/Video.webm", Width: 1920, Height: 1080},
				{URL: "https://images.scrolller.com/thumb-640x360.webp", Width: 640, Height: 360},
			},
			RedgifsSource: &str,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockScrolllerResponse(posts))
	}))
	defer srv.Close()

	client := newScrolllerTestClient(srv)
	result, err := client.FetchNewPosts("testsubreddit", time.Time{}, FetchCredentials{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 post, got %d", len(result))
	}

	m := result[0].MediaItems[0]
	if m.Type != MediaVideo {
		t.Errorf("Type = %q, want video", m.Type)
	}
	if m.URL != "https://octa.scrolller.com/Video.mp4" {
		t.Errorf("URL = %q, want highest-res mp4", m.URL)
	}
	if m.Thumbnail != "https://images.scrolller.com/thumb-640x360.webp" {
		t.Errorf("Thumbnail = %q, want webp thumb", m.Thumbnail)
	}
}

func TestScrolllerFetchNewPosts_NoMedia(t *testing.T) {
	posts := []scrolllerPost{
		{
			Typename:     "SubredditPost",
			ID:           99,
			URL:          "/no-media",
			Title:        "No Media Post",
			MediaSources: nil,
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockScrolllerResponse(posts))
	}))
	defer srv.Close()

	client := newScrolllerTestClient(srv)
	result, err := client.FetchNewPosts("testsubreddit", time.Time{}, FetchCredentials{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected 0 posts (no media), got %d", len(result))
	}
}

func TestScrolllerFetchNewPosts_GraphQLError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{
				{"message": "Subreddit not found"},
			},
		})
	}))
	defer srv.Close()

	client := newScrolllerTestClient(srv)
	_, err := client.FetchNewPosts("doesnotexist", time.Time{}, FetchCredentials{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScrolllerFetchNewPosts_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newScrolllerTestClient(srv)
	_, err := client.FetchNewPosts("testsubreddit", time.Time{}, FetchCredentials{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScrolllerMediaItem_WebmOnly(t *testing.T) {
	p := scrolllerPost{
		ID:    1,
		Title: "webm only",
		MediaSources: []scrolllerMediaSource{
			{URL: "https://proton.scrolller.com/Test.webm", Width: 1280, Height: 720},
		},
	}
	m := scrolllerMediaItem(p)
	if m == nil {
		t.Fatal("expected non-nil media item")
	}
	if m.Type != MediaVideo {
		t.Errorf("Type = %q, want video", m.Type)
	}
}

func TestSyntheticScrolllerTimestamp(t *testing.T) {
	now := time.Now().UTC()

	// Single item: always "now", no spread needed.
	if got := syntheticScrolllerTimestamp(0, 1, time.Time{}, now); !got.Equal(now) {
		t.Errorf("single item: want now, got %v", got)
	}

	// First-ever check (zero since): falls back to a 30-minute window.
	first := syntheticScrolllerTimestamp(0, 3, time.Time{}, now)
	last := syntheticScrolllerTimestamp(2, 3, time.Time{}, now)
	if !first.Equal(now) {
		t.Errorf("index 0: want now, got %v", first)
	}
	if !last.Equal(now.Add(-30 * time.Minute)) {
		t.Errorf("last index with zero since: want now-30m, got %v", last)
	}

	// Normal case: spreads linearly across [since, now].
	since := now.Add(-20 * time.Minute)
	mid := syntheticScrolllerTimestamp(2, 5, since, now)
	wantMid := now.Add(-10 * time.Minute) // index 2 of 5 -> halfway through the 20m window
	if !mid.Equal(wantMid) {
		t.Errorf("midpoint: want %v, got %v", wantMid, mid)
	}
	oldest := syntheticScrolllerTimestamp(4, 5, since, now)
	if !oldest.Equal(since) {
		t.Errorf("last index: want since (%v), got %v", since, oldest)
	}
}

func TestScrolllerFetchNewPosts_SpreadsTimestampsAcrossBatch(t *testing.T) {
	posts := make([]scrolllerPost, 5)
	for i := range posts {
		posts[i] = scrolllerPost{
			Typename: "SubredditPost",
			ID:       i + 1,
			URL:      fmt.Sprintf("/post-%d", i),
			Title:    fmt.Sprintf("Post %d", i),
			MediaSources: []scrolllerMediaSource{
				{URL: fmt.Sprintf("https://images.scrolller.com/%d.jpg", i), Width: 800, Height: 600},
			},
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockScrolllerResponse(posts))
	}))
	defer srv.Close()

	client := newScrolllerTestClient(srv)
	since := time.Now().UTC().Add(-10 * time.Minute)
	result, err := client.FetchNewPosts("testsubreddit", since, FetchCredentials{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 5 {
		t.Fatalf("want 5 posts, got %d", len(result))
	}

	// Timestamps must be strictly decreasing in fetch order (item 0 newest),
	// not all bunched at the same instant — this is what lets the UI
	// interleave posts from different subreddits/sources by real recency
	// instead of showing one solid block per subreddit.
	for i := 1; i < len(result); i++ {
		if !result[i-1].CreatedAt.After(result[i].CreatedAt) {
			t.Errorf("expected strictly decreasing CreatedAt, item %d (%v) not after item %d (%v)",
				i-1, result[i-1].CreatedAt, i, result[i].CreatedAt)
		}
	}
	if result[len(result)-1].CreatedAt.Before(since) {
		t.Errorf("oldest item's CreatedAt (%v) should not predate since (%v)", result[len(result)-1].CreatedAt, since)
	}
}

func TestFallbackFetcher_UsesSecondaryOnError(t *testing.T) {
	called := false
	secondary := &mockFetcher{
		fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
			called = true
			return []Post{{ID: "fallback_post"}}, nil
		},
	}
	primary := &mockFetcher{
		fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
			return nil, fmt.Errorf("primary failed")
		},
	}

	fb := &FallbackFetcher{Primary: primary, Secondary: secondary}
	posts, err := fb.FetchNewPosts("pics", time.Now(), FetchCredentials{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("secondary fetcher was not called")
	}
	if len(posts) != 1 || posts[0].ID != "fallback_post" {
		t.Errorf("unexpected posts: %v", posts)
	}
}

func TestFallbackFetcher_UsesPrimaryWhenSucceeds(t *testing.T) {
	secondaryCalled := false
	secondary := &mockFetcher{
		fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
			secondaryCalled = true
			return nil, nil
		},
	}
	primary := &mockFetcher{
		fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
			return []Post{{ID: "primary_post"}}, nil
		},
	}

	fb := &FallbackFetcher{Primary: primary, Secondary: secondary}
	posts, err := fb.FetchNewPosts("pics", time.Now(), FetchCredentials{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if secondaryCalled {
		t.Error("secondary fetcher should not have been called")
	}
	if len(posts) != 1 || posts[0].ID != "primary_post" {
		t.Errorf("unexpected posts: %v", posts)
	}
}
