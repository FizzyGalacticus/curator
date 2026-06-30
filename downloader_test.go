package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// ── extFromMediaItem ──────────────────────────────────────────────────────

func TestExtFromMediaItem_Video(t *testing.T) {
	got := extFromMediaItem(MediaItem{Type: MediaVideo, URL: "https://example.com/clip"})
	if got != ".mp4" {
		t.Errorf("want .mp4, got %s", got)
	}
}

func TestExtFromMediaItem_Gif(t *testing.T) {
	got := extFromMediaItem(MediaItem{Type: MediaGif, URL: "https://example.com/anim"})
	if got != ".gif" {
		t.Errorf("want .gif, got %s", got)
	}
}

func TestExtFromMediaItem_ImageFromURL(t *testing.T) {
	tests := map[string]string{
		"https://example.com/pic.png":          ".png",
		"https://example.com/pic.jpeg?v=1":     ".jpeg",
		"https://example.com/pic.WEBP":          ".webp",
		"https://example.com/pic_no_extension": ".jpg", // default
	}
	for url, want := range tests {
		got := extFromMediaItem(MediaItem{Type: MediaImage, URL: url})
		if got != want {
			t.Errorf("url=%s: want %s, got %s", url, want, got)
		}
	}
}

// ── sanitizeDirName ───────────────────────────────────────────────────────

func TestSanitizeDirName(t *testing.T) {
	tests := map[string]string{
		"pics":        "pics",
		"r/pics":      "r_pics",
		"my subreddit": "my_subreddit",
		"a-b_c123":    "a-b_c123",
		"日本語":         "___",
	}
	for in, want := range tests {
		got := sanitizeDirName(in)
		if got != want {
			t.Errorf("sanitizeDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── downloadFile ──────────────────────────────────────────────────────────

func TestDownloadFile_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake binary data"))
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.jpg")
	client := &http.Client{}
	if err := downloadFile(client, srv.URL+"/file.jpg", dest); err != nil {
		t.Fatalf("downloadFile: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "fake binary data" {
		t.Errorf("unexpected content: %s", data)
	}
}

func TestDownloadFile_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "out.jpg")
	client := &http.Client{}
	err := downloadFile(client, srv.URL+"/missing.jpg", dest)
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("file should not exist after failed download")
	}
}

// ── DownloadMedia (integration) ───────────────────────────────────────────

func TestDownloadMedia_WritesFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("content-for-" + r.URL.Path))
	}))
	defer srv.Close()

	downloadDir := t.TempDir()
	post := Post{
		ID:        "post123",
		Subreddit: "pics",
		MediaItems: []MediaItem{
			{Type: MediaImage, URL: srv.URL + "/0.jpg"},
			{Type: MediaImage, URL: srv.URL + "/1.jpg"},
		},
	}

	DownloadMedia(post, downloadDir)

	dir := filepath.Join(downloadDir, "pics", "post123")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 downloaded files, got %d", len(entries))
	}
}

func TestDownloadMedia_SkipsExisting(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte("data"))
	}))
	defer srv.Close()

	downloadDir := t.TempDir()
	post := Post{
		ID:         "post456",
		Subreddit:  "pics",
		MediaItems: []MediaItem{{Type: MediaImage, URL: srv.URL + "/0.jpg"}},
	}

	DownloadMedia(post, downloadDir)
	if hits != 1 {
		t.Fatalf("want 1 HTTP request on first download, got %d", hits)
	}

	DownloadMedia(post, downloadDir) // second call should skip existing file
	if hits != 1 {
		t.Errorf("want still 1 HTTP request after second call (file exists), got %d", hits)
	}
}

func TestDownloadMedia_EmptyDownloadDir(t *testing.T) {
	// Should be a no-op and not panic when downloadDir is empty.
	post := Post{ID: "p", Subreddit: "pics", MediaItems: []MediaItem{{Type: MediaImage, URL: "https://example.com/x.jpg"}}}
	DownloadMedia(post, "")
}
