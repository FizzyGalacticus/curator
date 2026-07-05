package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestRedditClient creates a RedditClient whose HTTP transports point at
// the given servers (use plain http.Client since test servers are HTTP, not HTTPS).
func newTestRedditClient(baseURL, redGifsBase, imgurBase string) *RedditClient {
	return &RedditClient{
		http:        &http.Client{},
		extHTTP:     &http.Client{},
		baseURL:     baseURL,
		redGifsBase: redGifsBase,
		imgurBase:   imgurBase,
	}
}

// ── Pure helpers ──────────────────────────────────────────────────────────

func TestIsDirectImageURL(t *testing.T) {
	yes := []string{
		"https://i.redd.it/abc.jpg",
		"https://example.com/photo.jpeg",
		"https://cdn.example.com/img.png",
		"https://example.com/anim.gif",
		"https://example.com/pic.webp",
		"https://example.com/img.JPG",       // uppercase
		"https://example.com/img.jpg?v=123", // query string stripped
	}
	no := []string{
		"https://www.reddit.com/r/pics",
		"https://v.redd.it/abc.mp4",
		"https://www.redgifs.com/watch/abc",
		"https://imgur.com/a/abc",
	}
	for _, u := range yes {
		if !isDirectImageURL(u) {
			t.Errorf("isDirectImageURL(%q) = false, want true", u)
		}
	}
	for _, u := range no {
		if isDirectImageURL(u) {
			t.Errorf("isDirectImageURL(%q) = true, want false", u)
		}
	}
}

func TestIsDirectVideoURL(t *testing.T) {
	yes := []string{
		"https://v.redd.it/clip.mp4",
		"https://example.com/vid.webm",
		"https://example.com/vid.MP4",
	}
	no := []string{
		"https://www.youtube.com/watch?v=abc",
		"https://example.com/pic.jpg",
	}
	for _, u := range yes {
		if !isDirectVideoURL(u) {
			t.Errorf("isDirectVideoURL(%q) = false, want true", u)
		}
	}
	for _, u := range no {
		if isDirectVideoURL(u) {
			t.Errorf("isDirectVideoURL(%q) = true, want false", u)
		}
	}
}

func TestIsGifURL(t *testing.T) {
	if !isGifURL("https://media.giphy.com/abc.gif") {
		t.Error("gif URL should return true")
	}
	if isGifURL("https://example.com/pic.jpg") {
		t.Error("jpg URL should return false")
	}
}

func TestPickThumbnailFromPreview_BestFit(t *testing.T) {
	img := redditPreviewImage{}
	img.Source.URL = "https://example.com/full.jpg"
	img.Source.Width = 1920
	img.Resolutions = []struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}{
		{URL: "https://example.com/320.jpg", Width: 320, Height: 240},
		{URL: "https://example.com/640.jpg", Width: 640, Height: 480},
		{URL: "https://example.com/960.jpg", Width: 960, Height: 720},
	}

	// target 640 → widest resolution within 2×640=1280 is 960.
	got := pickThumbnailFromPreview(img, 640)
	if got != "https://example.com/960.jpg" {
		t.Errorf("want 960.jpg, got %s", got)
	}

	// target 200 → best fit is 320 (400 threshold, 320 ≤ 400 and widest)
	got = pickThumbnailFromPreview(img, 200)
	if got != "https://example.com/320.jpg" {
		t.Errorf("want 320.jpg for target 200, got %s", got)
	}

	// No resolutions → falls back to source URL.
	img2 := redditPreviewImage{}
	img2.Source.URL = "https://example.com/full.jpg"
	got = pickThumbnailFromPreview(img2, 640)
	if got != "https://example.com/full.jpg" {
		t.Errorf("want fallback to source, got %s", got)
	}
}

// ── extractMedia: Reddit-native video ─────────────────────────────────────

func TestExtractMedia_RedditVideo(t *testing.T) {
	c := newTestRedditClient("", "", "")
	d := redditPostData{
		IsVideo: true,
		Media: &struct {
			RedditVideo *struct {
				FallbackURL string `json:"fallback_url"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
			} `json:"reddit_video,omitempty"`
			OEmbed *struct {
				ThumbnailURL    string `json:"thumbnail_url,omitempty"`
				ThumbnailWidth  int    `json:"thumbnail_width,omitempty"`
				ThumbnailHeight int    `json:"thumbnail_height,omitempty"`
				ProviderName    string `json:"provider_name,omitempty"`
			} `json:"oembed,omitempty"`
		}{
			RedditVideo: &struct {
				FallbackURL string `json:"fallback_url"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
			}{
				FallbackURL: "https://v.redd.it/abc/DASH_720.mp4",
				Width:       720,
				Height:      1280,
			},
		},
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Type != MediaVideo {
		t.Errorf("want MediaVideo, got %s", items[0].Type)
	}
	if items[0].URL != "https://v.redd.it/abc/DASH_720.mp4" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
	if items[0].Width != 720 || items[0].Height != 1280 {
		t.Errorf("unexpected dimensions: %dx%d", items[0].Width, items[0].Height)
	}
}

// ── extractMedia: Gallery ─────────────────────────────────────────────────

func TestExtractMedia_Gallery(t *testing.T) {
	c := newTestRedditClient("", "", "")
	d := redditPostData{
		IsGallery: true,
		GalleryData: &struct {
			Items []struct {
				MediaID string `json:"media_id"`
				ID      int    `json:"id"`
			} `json:"items"`
		}{
			Items: []struct {
				MediaID string `json:"media_id"`
				ID      int    `json:"id"`
			}{
				{MediaID: "img1"},
				{MediaID: "img2"},
			},
		},
		MediaMetadata: map[string]struct {
			Status string `json:"status"`
			E      string `json:"e"`
			M      string `json:"m"`
			S      struct {
				X   int    `json:"x"`
				Y   int    `json:"y"`
				U   string `json:"u"`
				MP4 string `json:"mp4,omitempty"`
				GIF string `json:"gif,omitempty"`
			} `json:"s"`
			P []struct {
				X int    `json:"x"`
				Y int    `json:"y"`
				U string `json:"u"`
			} `json:"p"`
		}{
			"img1": {
				Status: "valid",
				E:      "Image",
				S: struct {
					X   int    `json:"x"`
					Y   int    `json:"y"`
					U   string `json:"u"`
					MP4 string `json:"mp4,omitempty"`
					GIF string `json:"gif,omitempty"`
				}{X: 800, Y: 600, U: "https://i.redd.it/img1.jpg"},
			},
			"img2": {
				Status: "valid",
				E:      "Image",
				S: struct {
					X   int    `json:"x"`
					Y   int    `json:"y"`
					U   string `json:"u"`
					MP4 string `json:"mp4,omitempty"`
					GIF string `json:"gif,omitempty"`
				}{X: 1920, Y: 1080, U: "https://i.redd.it/img2.jpg"},
			},
		},
	}

	items := c.extractMedia(d, "")
	if len(items) != 2 {
		t.Fatalf("want 2 gallery items, got %d", len(items))
	}
	if items[0].URL != "https://i.redd.it/img1.jpg" {
		t.Errorf("item[0] URL: %s", items[0].URL)
	}
	if items[1].Width != 1920 {
		t.Errorf("item[1] Width: want 1920, got %d", items[1].Width)
	}
}

func TestExtractMedia_Gallery_AnimatedMP4(t *testing.T) {
	c := newTestRedditClient("", "", "")
	d := redditPostData{
		IsGallery: true,
		GalleryData: &struct {
			Items []struct {
				MediaID string `json:"media_id"`
				ID      int    `json:"id"`
			} `json:"items"`
		}{
			Items: []struct {
				MediaID string `json:"media_id"`
				ID      int    `json:"id"`
			}{{MediaID: "anim1"}},
		},
		MediaMetadata: map[string]struct {
			Status string `json:"status"`
			E      string `json:"e"`
			M      string `json:"m"`
			S      struct {
				X   int    `json:"x"`
				Y   int    `json:"y"`
				U   string `json:"u"`
				MP4 string `json:"mp4,omitempty"`
				GIF string `json:"gif,omitempty"`
			} `json:"s"`
			P []struct {
				X int    `json:"x"`
				Y int    `json:"y"`
				U string `json:"u"`
			} `json:"p"`
		}{
			"anim1": {
				Status: "valid",
				E:      "AnimatedImage",
				S: struct {
					X   int    `json:"x"`
					Y   int    `json:"y"`
					U   string `json:"u"`
					MP4 string `json:"mp4,omitempty"`
					GIF string `json:"gif,omitempty"`
				}{
					X:   640,
					Y:   480,
					U:   "https://i.redd.it/anim1.gif",
					MP4: "https://i.redd.it/anim1.mp4",
				},
			},
		},
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Type != MediaVideo {
		t.Errorf("animated with mp4 should be MediaVideo, got %s", items[0].Type)
	}
	if items[0].URL != "https://i.redd.it/anim1.mp4" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
}

// ── extractMedia: Single image ────────────────────────────────────────────

func TestExtractMedia_SingleImage(t *testing.T) {
	c := newTestRedditClient("", "", "")
	d := redditPostData{
		PostHint: "image",
		URL:      "https://i.redd.it/photo.jpg",
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Type != MediaImage {
		t.Errorf("want MediaImage, got %s", items[0].Type)
	}
}

func TestExtractMedia_SingleImage_MP4Variant(t *testing.T) {
	c := newTestRedditClient("", "", "")
	mp4URL := "https://preview.redd.it/anim.gif?format=mp4"
	d := redditPostData{
		PostHint: "image",
		URL:      "https://i.redd.it/anim.gif",
		Preview: &struct {
			Images             []redditPreviewImage `json:"images"`
			RedditVideoPreview *struct {
				FallbackURL string `json:"fallback_url"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
			} `json:"reddit_video_preview,omitempty"`
		}{
			Images: []redditPreviewImage{
				{
					Variants: &struct {
						MP4 *struct {
							Source struct {
								URL    string `json:"url"`
								Width  int    `json:"width"`
								Height int    `json:"height"`
							} `json:"source"`
						} `json:"mp4,omitempty"`
					}{
						MP4: &struct {
							Source struct {
								URL    string `json:"url"`
								Width  int    `json:"width"`
								Height int    `json:"height"`
							} `json:"source"`
						}{
							Source: struct {
								URL    string `json:"url"`
								Width  int    `json:"width"`
								Height int    `json:"height"`
							}{URL: mp4URL, Width: 640, Height: 480},
						},
					},
				},
			},
		},
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Type != MediaVideo {
		t.Errorf("gif with mp4 variant should be MediaVideo, got %s", items[0].Type)
	}
	if items[0].URL != mp4URL {
		t.Errorf("want mp4 URL, got %s", items[0].URL)
	}
}

// ── extractMedia: rich:video with Reddit's cached preview ─────────────────

func TestExtractMedia_RichVideo_RedditPreview(t *testing.T) {
	c := newTestRedditClient("", "", "")
	d := redditPostData{
		PostHint: "rich:video",
		URL:      "https://www.redgifs.com/watch/somethingneat",
		Preview: &struct {
			Images             []redditPreviewImage `json:"images"`
			RedditVideoPreview *struct {
				FallbackURL string `json:"fallback_url"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
			} `json:"reddit_video_preview,omitempty"`
		}{
			RedditVideoPreview: &struct {
				FallbackURL string `json:"fallback_url"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
			}{
				FallbackURL: "https://v.redd.it/cached/DASH_720.mp4",
				Width:       720,
				Height:      1280,
			},
		},
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].URL != "https://v.redd.it/cached/DASH_720.mp4" {
		t.Errorf("want cached Reddit CDN URL, got %s", items[0].URL)
	}
}

// ── extractMedia: rich:video → RedGifs API call ───────────────────────────

func TestExtractMedia_RichVideo_RedGifs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"gif": map[string]interface{}{
				"urls": map[string]interface{}{
					"hd":     "https://thumbs4.redgifs.com/CoolGif.mp4",
					"poster": "https://thumbs4.redgifs.com/CoolGif.jpg",
				},
				"width":  1080,
				"height": 1920,
			},
		})
	}))
	defer srv.Close()

	c := newTestRedditClient("", srv.URL, "")
	d := redditPostData{
		PostHint: "rich:video",
		URL:      "https://www.redgifs.com/watch/coolgif",
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Type != MediaVideo {
		t.Errorf("want MediaVideo, got %s", items[0].Type)
	}
	if items[0].URL != "https://thumbs4.redgifs.com/CoolGif.mp4" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
	if items[0].Width != 1080 {
		t.Errorf("unexpected width: %d", items[0].Width)
	}
}

// ── extractMedia: rich:video → oEmbed fallback ────────────────────────────

func TestExtractMedia_RichVideo_OEmbedFallback(t *testing.T) {
	c := newTestRedditClient("", "", "")
	d := redditPostData{
		PostHint: "rich:video",
		URL:      "https://streamable.com/abc",
		Media: &struct {
			RedditVideo *struct {
				FallbackURL string `json:"fallback_url"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
			} `json:"reddit_video,omitempty"`
			OEmbed *struct {
				ThumbnailURL    string `json:"thumbnail_url,omitempty"`
				ThumbnailWidth  int    `json:"thumbnail_width,omitempty"`
				ThumbnailHeight int    `json:"thumbnail_height,omitempty"`
				ProviderName    string `json:"provider_name,omitempty"`
			} `json:"oembed,omitempty"`
		}{
			OEmbed: &struct {
				ThumbnailURL    string `json:"thumbnail_url,omitempty"`
				ThumbnailWidth  int    `json:"thumbnail_width,omitempty"`
				ThumbnailHeight int    `json:"thumbnail_height,omitempty"`
				ProviderName    string `json:"provider_name,omitempty"`
			}{
				ThumbnailURL:    "https://cdn.streamable.com/thumb.jpg",
				ThumbnailWidth:  1280,
				ThumbnailHeight: 720,
			},
		},
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Type != MediaImage {
		t.Errorf("oEmbed fallback should be MediaImage, got %s", items[0].Type)
	}
	if items[0].URL != "https://cdn.streamable.com/thumb.jpg" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
}

// ── extractMedia: Imgur direct link ──────────────────────────────────────

func TestExtractMedia_Imgur_DirectLink(t *testing.T) {
	c := newTestRedditClient("", "", "")
	// i.imgur.com direct links are caught by isDirectImageURL before Imgur logic.
	d := redditPostData{
		PostHint: "image",
		URL:      "https://i.imgur.com/abcdef.jpg",
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].URL != "https://i.imgur.com/abcdef.jpg" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
}

// ── extractMedia: Imgur album via API ────────────────────────────────────

func TestExtractMedia_Imgur_Album(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"link": "https://i.imgur.com/img1.jpg", "width": 1920, "height": 1080},
				{"link": "https://i.imgur.com/img2.jpg", "width": 800, "height": 600},
			},
		})
	}))
	defer srv.Close()

	c := newTestRedditClient("", "", srv.URL)
	d := redditPostData{
		URL: "https://imgur.com/a/ABCDE",
	}

	items := c.extractMedia(d, "myclientid")
	if len(items) != 2 {
		t.Fatalf("want 2 album images, got %d", len(items))
	}
	if items[0].URL != "https://i.imgur.com/img1.jpg" {
		t.Errorf("item[0] URL: %s", items[0].URL)
	}
}

// ── extractMedia: direct video URL ───────────────────────────────────────

func TestExtractMedia_DirectVideoURL(t *testing.T) {
	c := newTestRedditClient("", "", "")
	d := redditPostData{
		URL: "https://cdn.example.com/clip.mp4",
	}

	items := c.extractMedia(d, "")
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	if items[0].Type != MediaVideo {
		t.Errorf("want MediaVideo, got %s", items[0].Type)
	}
}

// ── FetchNewPosts with mock Reddit RSS server ─────────────────────────────

// mockRSSFeed builds a minimal Atom feed XML string for testing.
// Each entry is: id, subreddit, title, author, permalink, mediaURL, thumbnail, published.
func mockRSSFeed(entries []struct{ id, sub, title, author, permalink, mediaURL, thumbnail, published string }) string {
	body := `<?xml version="1.0" encoding="UTF-8"?><feed xmlns="http://www.w3.org/2005/Atom" xmlns:media="http://search.yahoo.com/mrss/">`
	for _, e := range entries {
		body += fmt.Sprintf(`<entry>`+
			`<id>t3_%s</id>`+
			`<title>%s</title>`+
			`<author><name>/u/%s</name></author>`+
			`<category term="%s" label="r/%s"/>`+
			`<link href="%s"/>`+
			`<published>%s</published>`+
			`<updated>%s</updated>`+
			`<media:thumbnail url="%s"/>`,
			e.id, e.title, e.author, e.sub, e.sub,
			e.permalink, e.published, e.published, e.thumbnail)
		// Content HTML: [permalink, user, mediaURL, permalink] — same order Reddit uses.
		body += fmt.Sprintf(
			`<content type="html">`+
				`&lt;a href=&quot;%s&quot;&gt;[comments]&lt;/a&gt; `+
				`&lt;a href=&quot;https://www.reddit.com/user/%s&quot;&gt;/u/%s&lt;/a&gt; `+
				`&lt;a href=&quot;%s&quot;&gt;[link]&lt;/a&gt; `+
				`&lt;a href=&quot;%s&quot;&gt;[comments]&lt;/a&gt;`+
				`</content>`,
			e.permalink, e.author, e.author, e.mediaURL, e.permalink)
		body += `</entry>`
	}
	body += `</feed>`
	return body
}

func TestFetchNewPosts_MockServer(t *testing.T) {
	now := time.Now().UTC()
	feed := mockRSSFeed([]struct{ id, sub, title, author, permalink, mediaURL, thumbnail, published string }{
		{
			id: "post1", sub: "pics", title: "Test Image", author: "testuser",
			permalink: "https://www.reddit.com/r/pics/comments/post1/test/",
			mediaURL:  "https://i.redd.it/test.jpg",
			thumbnail: "https://preview.redd.it/test.jpg",
			published: now.Add(-1 * time.Hour).Format(time.RFC3339),
		},
		{
			// No recognisable media URL — should be skipped.
			id: "post2", sub: "pics", title: "Link Post", author: "other",
			permalink: "https://www.reddit.com/r/pics/comments/post2/link/",
			mediaURL:  "https://www.example.com/article",
			thumbnail: "",
			published: now.Add(-2 * time.Hour).Format(time.RFC3339),
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		fmt.Fprint(w, feed)
	}))
	defer srv.Close()

	c := newTestRedditClient(srv.URL, "", "")
	posts, err := c.FetchNewPosts("pics", time.Time{}, FetchCredentials{})
	if err != nil {
		t.Fatalf("FetchNewPosts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("want 1 post (no-media skipped), got %d", len(posts))
	}
	if posts[0].ID != "post1" {
		t.Errorf("unexpected post ID: %s", posts[0].ID)
	}
	if posts[0].Subreddit != "pics" {
		t.Errorf("unexpected subreddit: %s", posts[0].Subreddit)
	}
	if posts[0].Author != "testuser" {
		t.Errorf("unexpected author: %s", posts[0].Author)
	}
	if posts[0].Source != SourceReddit {
		t.Errorf("want Source=%q, got %q", SourceReddit, posts[0].Source)
	}
}

func TestFetchNewPosts_SinceFilter(t *testing.T) {
	now := time.Now().UTC()
	feed := mockRSSFeed([]struct{ id, sub, title, author, permalink, mediaURL, thumbnail, published string }{
		{
			id: "old", sub: "pics", title: "Old Post", author: "u1",
			permalink: "https://www.reddit.com/r/pics/comments/old/",
			mediaURL:  "https://i.redd.it/old.jpg",
			thumbnail: "",
			published: now.Add(-5 * time.Hour).Format(time.RFC3339),
		},
		{
			id: "new", sub: "pics", title: "New Post", author: "u2",
			permalink: "https://www.reddit.com/r/pics/comments/new/",
			mediaURL:  "https://i.redd.it/new.jpg",
			thumbnail: "",
			published: now.Add(-1 * time.Hour).Format(time.RFC3339),
		},
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, feed)
	}))
	defer srv.Close()

	c := newTestRedditClient(srv.URL, "", "")
	since := now.Add(-3 * time.Hour)
	posts, err := c.FetchNewPosts("pics", since, FetchCredentials{})
	if err != nil {
		t.Fatalf("FetchNewPosts: %v", err)
	}
	if len(posts) != 1 || posts[0].ID != "new" {
		t.Errorf("since filter: want [new], got %v", posts)
	}
}

// ── extractContentURL ─────────────────────────────────────────────────────

func TestExtractContentURL(t *testing.T) {
	// Simulate what Reddit's XML decoder produces after unescaping &quot; → "
	content := `<a href="https://www.reddit.com/r/pics/comments/abc/title/">[c]</a> ` +
		`<a href="https://www.reddit.com/user/bob">/u/bob</a> ` +
		`<a href="https://i.redd.it/abc.jpg">[link]</a> ` +
		`<a href="https://www.reddit.com/r/pics/comments/abc/title/">[c]</a>`
	got := extractContentURL(content)
	if got != "https://i.redd.it/abc.jpg" {
		t.Errorf("want i.redd.it URL, got %q", got)
	}
}

func TestExtractContentURL_TooFewLinks(t *testing.T) {
	content := `<a href="https://example.com/a">a</a> <a href="https://example.com/b">b</a>`
	if got := extractContentURL(content); got != "" {
		t.Errorf("want empty string for <3 links, got %q", got)
	}
}

// ── vredditMP4URL ─────────────────────────────────────────────────────────

func TestVredditMP4URL(t *testing.T) {
	got := vredditMP4URL("https://v.redd.it/abc123")
	if got != "https://v.redd.it/abc123/DASH_720.mp4" {
		t.Errorf("unexpected: %s", got)
	}
	// Already has a path suffix — should still use only the ID segment.
	got = vredditMP4URL("https://v.redd.it/xyz/DASH_360.mp4")
	if got != "https://v.redd.it/xyz/DASH_720.mp4" {
		t.Errorf("unexpected: %s", got)
	}
}

// ── rssMediaItems ─────────────────────────────────────────────────────────

func TestRSSMediaItems_DirectImage(t *testing.T) {
	c := newTestRedditClient("", "", "")
	items := c.rssMediaItems("https://i.redd.it/photo.jpg", "https://thumb.example.com/t.jpg", "")
	if len(items) != 1 || items[0].Type != MediaImage {
		t.Fatalf("want 1 image item, got %v", items)
	}
	if items[0].URL != "https://i.redd.it/photo.jpg" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
}

func TestRSSMediaItems_RedditVideo(t *testing.T) {
	c := newTestRedditClient("", "", "")
	items := c.rssMediaItems("https://v.redd.it/vid123", "https://thumb.example.com/t.jpg", "")
	if len(items) != 1 || items[0].Type != MediaVideo {
		t.Fatalf("want 1 video item, got %v", items)
	}
	if items[0].URL != "https://v.redd.it/vid123/DASH_720.mp4" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
}

func TestRSSMediaItems_Gallery(t *testing.T) {
	c := newTestRedditClient("", "", "")
	items := c.rssMediaItems("https://www.reddit.com/gallery/abc123", "https://preview.redd.it/t.jpg", "")
	if len(items) != 1 || items[0].Type != MediaImage {
		t.Fatalf("want 1 image item for gallery, got %v", items)
	}
	// Should use thumbnail as the image URL.
	if items[0].URL != "https://preview.redd.it/t.jpg" {
		t.Errorf("unexpected URL: %s", items[0].URL)
	}
}

func TestRSSMediaItems_UnknownURL(t *testing.T) {
	c := newTestRedditClient("", "", "")
	items := c.rssMediaItems("https://www.youtube.com/watch?v=abc", "", "")
	if len(items) != 0 {
		t.Errorf("want 0 items for unknown URL, got %v", items)
	}
}
