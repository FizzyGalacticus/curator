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
	fn func(subreddit string, since time.Time, imgurClientID string) ([]Post, error)
}

func (m *mockFetcher) FetchNewPosts(subreddit string, since time.Time, imgurClientID string) ([]Post, error) {
	return m.fn(subreddit, since, imgurClientID)
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(mockScrolllerResponse(posts))
	}))
	defer srv.Close()

	client := newScrolllerTestClient(srv)
	result, err := client.FetchNewPosts("testsubreddit", time.Time{}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
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
	result, err := client.FetchNewPosts("testsubreddit", time.Time{}, "")
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
	result, err := client.FetchNewPosts("testsubreddit", time.Time{}, "")
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
	_, err := client.FetchNewPosts("doesnotexist", time.Time{}, "")
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
	_, err := client.FetchNewPosts("testsubreddit", time.Time{}, "")
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

func TestFallbackFetcher_UsesSecondaryOnError(t *testing.T) {
	called := false
	secondary := &mockFetcher{
		fn: func(sub string, since time.Time, imgurID string) ([]Post, error) {
			called = true
			return []Post{{ID: "fallback_post"}}, nil
		},
	}
	primary := &mockFetcher{
		fn: func(sub string, since time.Time, imgurID string) ([]Post, error) {
			return nil, fmt.Errorf("primary failed")
		},
	}

	fb := &FallbackFetcher{Primary: primary, Secondary: secondary}
	posts, err := fb.FetchNewPosts("pics", time.Now(), "")
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
		fn: func(sub string, since time.Time, imgurID string) ([]Post, error) {
			secondaryCalled = true
			return nil, nil
		},
	}
	primary := &mockFetcher{
		fn: func(sub string, since time.Time, imgurID string) ([]Post, error) {
			return []Post{{ID: "primary_post"}}, nil
		},
	}

	fb := &FallbackFetcher{Primary: primary, Secondary: secondary}
	posts, err := fb.FetchNewPosts("pics", time.Now(), "")
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
