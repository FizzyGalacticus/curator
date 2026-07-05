package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// newFlickrTestClient creates a FlickrClient whose requests all go to srv.
func newFlickrTestClient(srv *httptest.Server) *FlickrClient {
	return &FlickrClient{
		http:     &http.Client{},
		baseURL:  srv.URL,
		groupIDs: map[string]string{},
	}
}

// flickrTestServer routes on the "method" query param, mirroring Flickr's
// single REST endpoint. lookupCalls counts how many times lookupGroup was hit.
func flickrTestServer(t *testing.T, groupID string, photos []flickrPhoto) (*httptest.Server, *int) {
	t.Helper()
	lookupCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("method") {
		case "flickr.urls.lookupGroup":
			lookupCalls++
			resp := flickrLookupGroupResponse{Stat: "ok"}
			resp.Group.ID = groupID
			json.NewEncoder(w).Encode(resp)
		case "flickr.groups.pools.getPhotos":
			resp := flickrPoolPhotosResponse{Stat: "ok"}
			resp.Photos.Photo = photos
			json.NewEncoder(w).Encode(resp)
		default:
			t.Fatalf("unexpected method %q", r.URL.Query().Get("method"))
		}
	}))
	return srv, &lookupCalls
}

func TestFlickrFetchNewPosts_ResolvesGroupAndReturnsPhotos(t *testing.T) {
	photos := []flickrPhoto{
		{
			ID: "123", Owner: "12345@N00", Title: "A photo", Media: "photo",
			DateUpload:  "1700000000",
			OwnerName:   "someuser",
			URLOriginal: "https://live.staticflickr.com/123_o.jpg", WidthOriginal: "1600", HeightOriginal: "1200",
			URLSquare: "https://live.staticflickr.com/123_sq.jpg",
		},
	}
	srv, _ := flickrTestServer(t, "51035612836@N01", photos)
	defer srv.Close()

	client := newFlickrTestClient(srv)
	posts, err := client.FetchNewPosts("blackandwhite", time.Time{}, FetchCredentials{FlickrAPIKey: "testkey"})
	if err != nil {
		t.Fatalf("FetchNewPosts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("want 1 post, got %d", len(posts))
	}

	p := posts[0]
	if p.ID != "flickr_123" {
		t.Errorf("ID = %q, want flickr_123", p.ID)
	}
	if p.Source != SourceFlickr {
		t.Errorf("Source = %q, want %q", p.Source, SourceFlickr)
	}
	if p.Subreddit != "blackandwhite" {
		t.Errorf("Subreddit = %q, want blackandwhite", p.Subreddit)
	}
	if p.Author != "someuser" {
		t.Errorf("Author = %q, want someuser", p.Author)
	}
	if p.Permalink != "https://www.flickr.com/photos/12345@N00/123/" {
		t.Errorf("Permalink = %q", p.Permalink)
	}
	if len(p.MediaItems) != 1 || p.MediaItems[0].URL != "https://live.staticflickr.com/123_o.jpg" {
		t.Fatalf("unexpected media items: %v", p.MediaItems)
	}
	if p.MediaItems[0].Width != 1600 || p.MediaItems[0].Height != 1200 {
		t.Errorf("dimensions = %dx%d, want 1600x1200", p.MediaItems[0].Width, p.MediaItems[0].Height)
	}
}

func TestFlickrFetchNewPosts_CachesGroupIDResolution(t *testing.T) {
	photos := []flickrPhoto{{ID: "1", Owner: "o@N00", Media: "photo", DateUpload: "1700000000", URLOriginal: "https://example.com/1_o.jpg"}}
	srv, lookupCalls := flickrTestServer(t, "somegroupid", photos)
	defer srv.Close()

	client := newFlickrTestClient(srv)
	creds := FetchCredentials{FlickrAPIKey: "testkey"}

	if _, err := client.FetchNewPosts("blackandwhite", time.Time{}, creds); err != nil {
		t.Fatalf("first FetchNewPosts: %v", err)
	}
	if _, err := client.FetchNewPosts("blackandwhite", time.Time{}, creds); err != nil {
		t.Fatalf("second FetchNewPosts: %v", err)
	}

	if *lookupCalls != 1 {
		t.Errorf("lookupGroup calls = %d, want 1 (cached after first resolution)", *lookupCalls)
	}
}

func TestFlickrFetchNewPosts_SkipsVideos(t *testing.T) {
	photos := []flickrPhoto{
		{ID: "1", Media: "video", DateUpload: "1700000000", URLOriginal: "https://example.com/1_o.mp4"},
		{ID: "2", Media: "photo", DateUpload: "1700000000", URLOriginal: "https://example.com/2_o.jpg"},
	}
	srv, _ := flickrTestServer(t, "gid", photos)
	defer srv.Close()

	client := newFlickrTestClient(srv)
	posts, err := client.FetchNewPosts("blackandwhite", time.Time{}, FetchCredentials{FlickrAPIKey: "testkey"})
	if err != nil {
		t.Fatalf("FetchNewPosts: %v", err)
	}
	if len(posts) != 1 || posts[0].ID != "flickr_2" {
		t.Errorf("want only the photo kept, got %v", posts)
	}
}

func TestFlickrFetchNewPosts_NoAPIKey_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no HTTP call should be made without an API key")
	}))
	defer srv.Close()

	client := newFlickrTestClient(srv)
	_, err := client.FetchNewPosts("blackandwhite", time.Time{}, FetchCredentials{})
	if err == nil {
		t.Fatal("expected error when no API key is configured")
	}
}

func TestFlickrFetchNewPosts_SinceFiltersOlderPhotos(t *testing.T) {
	now := time.Now().UTC()
	photos := []flickrPhoto{
		{ID: "old", Media: "photo", DateUpload: strconv.FormatInt(now.Add(-5*time.Hour).Unix(), 10), URLOriginal: "https://example.com/old_o.jpg"},
		{ID: "new", Media: "photo", DateUpload: strconv.FormatInt(now.Add(-1*time.Hour).Unix(), 10), URLOriginal: "https://example.com/new_o.jpg"},
	}
	srv, _ := flickrTestServer(t, "gid", photos)
	defer srv.Close()

	client := newFlickrTestClient(srv)
	since := now.Add(-3 * time.Hour)
	posts, err := client.FetchNewPosts("blackandwhite", since, FetchCredentials{FlickrAPIKey: "testkey"})
	if err != nil {
		t.Fatalf("FetchNewPosts: %v", err)
	}
	if len(posts) != 1 || posts[0].ID != "flickr_new" {
		t.Errorf("since filter: want only the newer photo, got %v", posts)
	}
}

func TestFlickrPhotoMediaItem_PrefersOriginalOverLargeOverMedium(t *testing.T) {
	// Original present -> used.
	p := flickrPhoto{URLOriginal: "orig.jpg", WidthOriginal: "100", HeightOriginal: "200", URLLarge: "large.jpg", URLMedium: "med.jpg"}
	if got := flickrPhotoMediaItem(p); got == nil || got.URL != "orig.jpg" {
		t.Errorf("want orig.jpg, got %+v", got)
	}

	// No original -> falls back to large.
	p2 := flickrPhoto{URLLarge: "large.jpg", URLMedium: "med.jpg"}
	if got := flickrPhotoMediaItem(p2); got == nil || got.URL != "large.jpg" {
		t.Errorf("want large.jpg, got %+v", got)
	}

	// Only medium -> uses medium.
	p3 := flickrPhoto{URLMedium: "med.jpg"}
	if got := flickrPhotoMediaItem(p3); got == nil || got.URL != "med.jpg" {
		t.Errorf("want med.jpg, got %+v", got)
	}

	// Nothing at all -> nil.
	if got := flickrPhotoMediaItem(flickrPhoto{}); got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}
