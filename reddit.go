package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const redditUserAgent = "reddit-curator:v1.0 (self-hosted media curator)"

// httpDoer abstracts *http.Client so tests can inject a stub transport.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// RedditClient fetches posts from Reddit's public JSON API.
type RedditClient struct {
	http        httpDoer
	extHTTP     httpDoer // shorter timeout for external API calls (RedGifs, Imgur)
	baseURL     string   // overridable in tests; production: "https://www.reddit.com"
	redGifsBase string   // overridable in tests; production: "https://api.redgifs.com"
	imgurBase   string   // overridable in tests; production: "https://api.imgur.com"
}

// NewRedditClient creates a client with sensible timeouts.
func NewRedditClient() *RedditClient {
	return &RedditClient{
		http:        &http.Client{Timeout: 30 * time.Second},
		extHTTP:     &http.Client{Timeout: 10 * time.Second},
		baseURL:     "https://www.reddit.com",
		redGifsBase: "https://api.redgifs.com",
		imgurBase:   "https://api.imgur.com",
	}
}

// ---- Atom/RSS feed types (Reddit public RSS endpoint) ----

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Entries []atomEntry `xml:"http://www.w3.org/2005/Atom entry"`
}

type atomEntry struct {
	ID        string `xml:"http://www.w3.org/2005/Atom id"`
	Title     string `xml:"http://www.w3.org/2005/Atom title"`
	Published string `xml:"http://www.w3.org/2005/Atom published"`
	Updated   string `xml:"http://www.w3.org/2005/Atom updated"`
	Author    struct {
		Name string `xml:"http://www.w3.org/2005/Atom name"`
	} `xml:"http://www.w3.org/2005/Atom author"`
	Category struct {
		Term string `xml:"term,attr"`
	} `xml:"http://www.w3.org/2005/Atom category"`
	Link struct {
		Href string `xml:"href,attr"`
	} `xml:"http://www.w3.org/2005/Atom link"`
	Content   string `xml:"http://www.w3.org/2005/Atom content"`
	Thumbnail struct {
		URL    string `xml:"url,attr"`
		Width  int    `xml:"width,attr"`
		Height int    `xml:"height,attr"`
	} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
}

// ---- Reddit JSON response types ----

type redditListing struct {
	Data struct {
		After    string `json:"after"`
		Children []struct {
			Kind string         `json:"kind"`
			Data redditPostData `json:"data"`
		} `json:"children"`
	} `json:"data"`
}

type redditPreviewImage struct {
	Source struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"source"`
	Resolutions []struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"resolutions"`
	Variants *struct {
		MP4 *struct {
			Source struct {
				URL    string `json:"url"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"source"`
		} `json:"mp4,omitempty"`
	} `json:"variants,omitempty"`
}

type redditPostData struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"` // fullname: t3_{id}
	Title      string  `json:"title"`
	Author     string  `json:"author"`
	Subreddit  string  `json:"subreddit"`
	Score      int     `json:"score"`
	CreatedUTC float64 `json:"created_utc"`
	Permalink  string  `json:"permalink"`
	URL        string  `json:"url"`
	PostHint   string  `json:"post_hint"`
	IsVideo    bool    `json:"is_video"`
	IsGallery  bool    `json:"is_gallery"`
	Domain     string  `json:"domain"`

	Preview *struct {
		Images []redditPreviewImage `json:"images"`
		// Reddit sometimes caches external videos here.
		RedditVideoPreview *struct {
			FallbackURL string `json:"fallback_url"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
		} `json:"reddit_video_preview,omitempty"`
	} `json:"preview,omitempty"`

	GalleryData *struct {
		Items []struct {
			MediaID string `json:"media_id"`
			ID      int    `json:"id"`
		} `json:"items"`
	} `json:"gallery_data,omitempty"`

	MediaMetadata map[string]struct {
		Status string `json:"status"`
		E      string `json:"e"` // "Image", "AnimatedImage", "RedditVideo"
		M      string `json:"m"` // MIME type
		S      struct {
			X   int    `json:"x"` // width
			Y   int    `json:"y"` // height
			U   string `json:"u"` // full-size URL
			MP4 string `json:"mp4,omitempty"`
			GIF string `json:"gif,omitempty"`
		} `json:"s"`
		P []struct {
			X int    `json:"x"`
			Y int    `json:"y"`
			U string `json:"u"`
		} `json:"p"`
	} `json:"media_metadata,omitempty"`

	Media *struct {
		RedditVideo *struct {
			FallbackURL string `json:"fallback_url"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
		} `json:"reddit_video,omitempty"`
		// Present for external embeds (RedGifs, Streamable, etc.)
		OEmbed *struct {
			ThumbnailURL    string `json:"thumbnail_url,omitempty"`
			ThumbnailWidth  int    `json:"thumbnail_width,omitempty"`
			ThumbnailHeight int    `json:"thumbnail_height,omitempty"`
			ProviderName    string `json:"provider_name,omitempty"`
		} `json:"oembed,omitempty"`
	} `json:"media,omitempty"`

	CrosspostParentList []redditPostData `json:"crosspost_parent_list,omitempty"`
}

// FetchNewPosts retrieves posts from a subreddit (or combined "a+b+c" multireddit)
// published after `since`. imgurClientID is optional; when set, Imgur albums are
// fully expanded.
func (c *RedditClient) FetchNewPosts(subreddit string, since time.Time, imgurClientID string) ([]Post, error) {
	url := fmt.Sprintf("%s/r/%s/new.rss?limit=100", c.baseURL, subreddit)

	feed, err := c.fetchRSSFeed(url)
	if err != nil {
		return nil, fmt.Errorf("fetching r/%s: %w", subreddit, err)
	}

	var posts []Post
	for _, entry := range feed.Entries {
		post, ok := c.parseRSSEntry(entry, imgurClientID)
		if !ok {
			continue
		}
		if !since.IsZero() && !post.CreatedAt.After(since) {
			continue
		}
		posts = append(posts, post)
	}

	log.Printf("r/%s: %d RSS entries → %d media posts (since %s)", subreddit, len(feed.Entries), len(posts), since.Format(time.RFC3339))
	return posts, nil
}

func (c *RedditClient) fetchRSSFeed(url string) (*atomFeed, error) {
	const maxAttempts = 3
	wait := 15 * time.Second

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", redditUserAgent)
		// over18=1 unlocks NSFW subreddits; pref_quarantine_optin covers quarantined subs.
		req.Header.Set("Cookie", `over18=1; _options={"pref_quarantine_optin":true}`)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			delay := wait
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
					delay = time.Duration(secs) * time.Second
				}
			}
			resp.Body.Close()
			if attempt < maxAttempts {
				log.Printf("r/%s: rate limited; retrying in %v (attempt %d/%d)", url, delay, attempt, maxAttempts)
				time.Sleep(delay)
				wait *= 2
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var feed atomFeed
		err = xml.NewDecoder(resp.Body).Decode(&feed)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decoding RSS feed: %w", err)
		}
		return &feed, nil
	}
	return nil, fmt.Errorf("rate limited after %d attempts", maxAttempts)
}

// parsePost converts a Reddit API post into our internal Post type.
// Returns (post, true) when the post contains at least one media item.
func (c *RedditClient) parsePost(d redditPostData, imgurClientID string) (Post, bool) {
	// For crossposts, use the parent's media but keep this post's metadata.
	if len(d.CrosspostParentList) > 0 {
		parent := d.CrosspostParentList[0]
		parent.ID = d.ID
		parent.Name = d.Name
		parent.Title = d.Title
		parent.Author = d.Author
		parent.Subreddit = d.Subreddit
		parent.Score = d.Score
		parent.CreatedUTC = d.CreatedUTC
		parent.Permalink = d.Permalink
		d = parent
	}

	items := c.extractMedia(d, imgurClientID)
	if len(items) == 0 {
		return Post{}, false
	}

	return Post{
		ID:           d.ID,
		Subreddit:    d.Subreddit,
		Title:        d.Title,
		Author:       d.Author,
		Score:        d.Score,
		CreatedAt:    time.Unix(int64(d.CreatedUTC), 0).UTC(),
		Permalink:    c.baseURL + d.Permalink,
		MediaItems:   items,
		DiscoveredAt: time.Now().UTC(),
	}, true
}

// extractMedia pulls all displayable media items from a post's raw data.
// Resolution order: Reddit-native → gallery → single image/gif → external video cache
// → RedGifs API → Imgur → direct URL.
func (c *RedditClient) extractMedia(d redditPostData, imgurClientID string) []MediaItem {
	// ── 1. Reddit-hosted video ────────────────────────────────────────────
	if d.IsVideo && d.Media != nil && d.Media.RedditVideo != nil {
		rv := d.Media.RedditVideo
		thumb := previewThumb(d, 640)
		return []MediaItem{{
			Type:      MediaVideo,
			URL:       rv.FallbackURL,
			Thumbnail: thumb,
			Width:     rv.Width,
			Height:    rv.Height,
		}}
	}

	// ── 2. Gallery ────────────────────────────────────────────────────────
	if d.IsGallery && d.GalleryData != nil && d.MediaMetadata != nil {
		items := c.extractGallery(d)
		if len(items) > 0 {
			return items
		}
	}

	// ── 3. Single image (post_hint == "image" or direct image URL) ────────
	if d.PostHint == "image" || isDirectImageURL(d.URL) {
		if item := c.singleImageItem(d); item != nil {
			return []MediaItem{*item}
		}
	}

	// ── 4. External video with Reddit's cached preview (rich:video) ───────
	// This covers RedGifs, Streamable, and many other embeds when Reddit
	// has already transcoded a copy into its own CDN.
	if d.PostHint == "rich:video" || d.PostHint == "hosted:video" {
		if items := c.extractRichVideo(d); len(items) > 0 {
			return items
		}
	}

	// ── 5. Imgur ──────────────────────────────────────────────────────────
	if isImgurURL(d.URL) {
		if items := c.extractImgur(d, imgurClientID); len(items) > 0 {
			return items
		}
	}

	// ── 6. Direct video URL ───────────────────────────────────────────────
	if isDirectVideoURL(d.URL) {
		return []MediaItem{{
			Type:      MediaVideo,
			URL:       d.URL,
			Thumbnail: previewThumb(d, 640),
		}}
	}

	// ── 7. Any post with a preview image (catch-all for link posts) ───────
	// Many "link" posts to image hosts still have a Reddit preview that's
	// usable for display even if we can't get the source.
	if d.PostHint == "link" && d.Preview != nil && len(d.Preview.Images) > 0 {
		img := d.Preview.Images[0]
		if img.Source.URL != "" {
			return []MediaItem{{
				Type:      MediaImage,
				URL:       img.Source.URL,
				Thumbnail: pickThumbnailFromPreview(img, 640),
				Width:     img.Source.Width,
				Height:    img.Source.Height,
			}}
		}
	}

	return nil
}

// ── Gallery ───────────────────────────────────────────────────────────────

func (c *RedditClient) extractGallery(d redditPostData) []MediaItem {
	var items []MediaItem
	for _, gi := range d.GalleryData.Items {
		meta, ok := d.MediaMetadata[gi.MediaID]
		if !ok || meta.Status != "valid" || meta.S.U == "" {
			continue
		}

		mediaType := MediaImage
		url := meta.S.U
		thumb := pickThumbnailFromPList(meta.P, 640)

		if meta.E == "AnimatedImage" {
			if meta.S.MP4 != "" {
				mediaType = MediaVideo
				url = meta.S.MP4
			} else if meta.S.GIF != "" {
				mediaType = MediaGif
				url = meta.S.GIF
			}
		}

		items = append(items, MediaItem{
			Type:      mediaType,
			URL:       url,
			Thumbnail: thumb,
			Width:     meta.S.X,
			Height:    meta.S.Y,
		})
	}
	return items
}

// ── Single image ──────────────────────────────────────────────────────────

func (c *RedditClient) singleImageItem(d redditPostData) *MediaItem {
	url := d.URL
	if url == "" {
		return nil
	}

	mediaType := MediaImage
	thumb := ""
	width, height := 0, 0

	if d.Preview != nil && len(d.Preview.Images) > 0 {
		img := d.Preview.Images[0]
		width = img.Source.Width
		height = img.Source.Height
		thumb = pickThumbnailFromPreview(img, 640)
		// Prefer the Reddit-hosted mp4 variant for GIFs.
		if img.Variants != nil && img.Variants.MP4 != nil && img.Variants.MP4.Source.URL != "" {
			return &MediaItem{
				Type:      MediaVideo,
				URL:       img.Variants.MP4.Source.URL,
				Thumbnail: thumb,
				Width:     width,
				Height:    height,
			}
		}
	}

	if isGifURL(url) {
		mediaType = MediaGif
	}

	return &MediaItem{
		Type:      mediaType,
		URL:       url,
		Thumbnail: thumb,
		Width:     width,
		Height:    height,
	}
}

// ── Rich/external video ───────────────────────────────────────────────────

func (c *RedditClient) extractRichVideo(d redditPostData) []MediaItem {
	thumb := previewThumb(d, 640)

	// Reddit's cached mp4 copy of an external video is the highest-fidelity
	// option and requires no external call.
	if d.Preview != nil && d.Preview.RedditVideoPreview != nil {
		rvp := d.Preview.RedditVideoPreview
		if rvp.FallbackURL != "" {
			return []MediaItem{{
				Type:      MediaVideo,
				URL:       rvp.FallbackURL,
				Thumbnail: thumb,
				Width:     rvp.Width,
				Height:    rvp.Height,
			}}
		}
	}

	// RedGifs: public API, no auth required.
	if strings.Contains(d.URL, "redgifs.com/watch/") || strings.Contains(d.URL, "redgifs.com/ifr/") {
		if item := c.fetchRedGifsItem(d.URL, thumb); item != nil {
			return []MediaItem{*item}
		}
	}

	// Fall back to oEmbed thumbnail as a static image preview.
	if d.Media != nil && d.Media.OEmbed != nil && d.Media.OEmbed.ThumbnailURL != "" {
		oe := d.Media.OEmbed
		return []MediaItem{{
			Type:      MediaImage,
			URL:       oe.ThumbnailURL,
			Thumbnail: oe.ThumbnailURL,
			Width:     oe.ThumbnailWidth,
			Height:    oe.ThumbnailHeight,
		}}
	}

	// Last resort: use the Reddit preview image.
	if thumb != "" && d.Preview != nil && len(d.Preview.Images) > 0 {
		img := d.Preview.Images[0]
		return []MediaItem{{
			Type:      MediaImage,
			URL:       img.Source.URL,
			Thumbnail: thumb,
			Width:     img.Source.Width,
			Height:    img.Source.Height,
		}}
	}

	return nil
}

// ── RedGifs ───────────────────────────────────────────────────────────────

func (c *RedditClient) fetchRedGifsItem(gifURL, fallbackThumb string) *MediaItem {
	// Extract slug: redgifs.com/watch/{slug} or redgifs.com/ifr/{slug}
	slug := ""
	for _, prefix := range []string{"redgifs.com/watch/", "redgifs.com/ifr/"} {
		if idx := strings.Index(strings.ToLower(gifURL), prefix); idx >= 0 {
			slug = gifURL[idx+len(prefix):]
			break
		}
	}
	if slug == "" {
		return nil
	}
	slug = strings.Split(slug, "?")[0]
	slug = strings.Split(slug, "#")[0]
	slug = strings.ToLower(slug)

	req, err := http.NewRequest(http.MethodGet, c.redGifsBase+"/v2/gifs/"+slug, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", redditUserAgent)

	resp, err := c.extHTTP.Do(req)
	if err != nil {
		log.Printf("RedGifs API error for %s: %v", slug, err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		GIF struct {
			URLs struct {
				HD        string `json:"hd"`
				SD        string `json:"sd"`
				Poster    string `json:"poster"`
				Thumbnail string `json:"thumbnail"`
			} `json:"urls"`
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"gif"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	videoURL := result.GIF.URLs.HD
	if videoURL == "" {
		videoURL = result.GIF.URLs.SD
	}
	if videoURL == "" {
		return nil
	}

	thumb := result.GIF.URLs.Poster
	if thumb == "" {
		thumb = result.GIF.URLs.Thumbnail
	}
	if thumb == "" {
		thumb = fallbackThumb
	}

	return &MediaItem{
		Type:      MediaVideo,
		URL:       videoURL,
		Thumbnail: thumb,
		Width:     result.GIF.Width,
		Height:    result.GIF.Height,
	}
}

// ── Imgur ─────────────────────────────────────────────────────────────────

func isImgurURL(u string) bool {
	return strings.Contains(strings.ToLower(u), "imgur.com")
}

func (c *RedditClient) extractImgur(d redditPostData, clientID string) []MediaItem {
	u := strings.ToLower(d.URL)

	// i.imgur.com direct link — already caught by isDirectImageURL, but
	// handle here as fallback.
	if strings.HasPrefix(u, "https://i.imgur.com/") || strings.HasPrefix(u, "http://i.imgur.com/") {
		if item := c.singleImageItem(d); item != nil {
			return []MediaItem{*item}
		}
	}

	// Album: imgur.com/a/{hash} or imgur.com/gallery/{hash}
	albumHash := ""
	if idx := strings.Index(u, "imgur.com/a/"); idx >= 0 {
		albumHash = d.URL[idx+len("imgur.com/a/"):]
	} else if idx := strings.Index(u, "imgur.com/gallery/"); idx >= 0 {
		albumHash = d.URL[idx+len("imgur.com/gallery/"):]
	}
	albumHash = strings.Split(albumHash, "?")[0]
	albumHash = strings.Split(albumHash, "/")[0]

	if albumHash != "" && clientID != "" {
		if items := c.fetchImgurAlbum(albumHash, clientID, previewThumb(d, 640)); len(items) > 0 {
			return items
		}
	}

	// Single imgur page (imgur.com/{id}), or album without a client_id.
	// Use Reddit's preview image — it's Reddit-cached so it works without
	// hitting Imgur's servers.
	if d.Preview != nil && len(d.Preview.Images) > 0 {
		img := d.Preview.Images[0]
		if img.Source.URL != "" {
			return []MediaItem{{
				Type:      MediaImage,
				URL:       img.Source.URL,
				Thumbnail: pickThumbnailFromPreview(img, 640),
				Width:     img.Source.Width,
				Height:    img.Source.Height,
			}}
		}
	}

	// Last resort: construct i.imgur.com URL for single-image pages.
	rest := d.URL
	for _, pfx := range []string{"https://imgur.com/", "http://imgur.com/"} {
		rest = strings.TrimPrefix(rest, pfx)
	}
	imgurID := strings.Split(rest, ".")[0]
	imgurID = strings.Split(imgurID, "?")[0]
	if imgurID != "" && !strings.Contains(imgurID, "/") {
		return []MediaItem{{
			Type: MediaImage,
			URL:  "https://i.imgur.com/" + imgurID + ".jpg",
		}}
	}

	return nil
}

func (c *RedditClient) fetchImgurAlbum(hash, clientID, fallbackThumb string) []MediaItem {
	req, err := http.NewRequest(http.MethodGet, c.imgurBase+"/3/album/"+hash+"/images", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Client-ID "+clientID)
	req.Header.Set("User-Agent", redditUserAgent)

	resp, err := c.extHTTP.Do(req)
	if err != nil {
		log.Printf("Imgur API error for album %s: %v", hash, err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Imgur API returned HTTP %d for album %s", resp.StatusCode, hash)
		return nil
	}

	var result struct {
		Data []struct {
			Link     string `json:"link"`
			MP4      string `json:"mp4,omitempty"`
			Animated bool   `json:"animated"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}

	var items []MediaItem
	for _, img := range result.Data {
		mediaType := MediaImage
		url := img.Link
		if img.Animated && img.MP4 != "" {
			mediaType = MediaVideo
			url = img.MP4
		} else if img.Animated && isGifURL(img.Link) {
			mediaType = MediaGif
		}

		items = append(items, MediaItem{
			Type:      mediaType,
			URL:       url,
			Thumbnail: fallbackThumb,
			Width:     img.Width,
			Height:    img.Height,
		})
	}
	return items
}

// ── Shared helpers ────────────────────────────────────────────────────────

// previewThumb returns the best thumbnail URL from the post's preview images.
func previewThumb(d redditPostData, targetWidth int) string {
	if d.Preview == nil || len(d.Preview.Images) == 0 {
		return ""
	}
	return pickThumbnailFromPreview(d.Preview.Images[0], targetWidth)
}

// pickThumbnailFromPreview selects the resolution entry closest to targetWidth
// but not exceeding 2× target. Falls back to the source URL.
func pickThumbnailFromPreview(img redditPreviewImage, targetWidth int) string {
	best := ""
	bestW := 0
	for _, r := range img.Resolutions {
		if r.URL == "" {
			continue
		}
		if best == "" || (r.Width <= targetWidth*2 && r.Width > bestW) {
			best = r.URL
			bestW = r.Width
		}
	}
	if best == "" {
		return img.Source.URL
	}
	return best
}

// pickThumbnailFromPList picks the best resolution from a media_metadata p[] list.
func pickThumbnailFromPList(pList []struct {
	X int    `json:"x"`
	Y int    `json:"y"`
	U string `json:"u"`
}, targetWidth int) string {
	best := ""
	bestW := 0
	for _, p := range pList {
		if p.U == "" {
			continue
		}
		if best == "" || (p.X <= targetWidth*2 && p.X > bestW) {
			best = p.U
			bestW = p.X
		}
	}
	return best
}

func isDirectImageURL(u string) bool {
	lower := strings.ToLower(strings.Split(u, "?")[0])
	return strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".jpeg") ||
		strings.HasSuffix(lower, ".png") ||
		strings.HasSuffix(lower, ".webp") ||
		strings.HasSuffix(lower, ".gif")
}

func isDirectVideoURL(u string) bool {
	lower := strings.ToLower(strings.Split(u, "?")[0])
	return strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".webm")
}

func isGifURL(u string) bool {
	return strings.HasSuffix(strings.ToLower(strings.Split(u, "?")[0]), ".gif")
}

// ---- RSS entry parsing -------------------------------------------------------

// hrefRe extracts href attribute values from HTML content.
var hrefRe = regexp.MustCompile(`href="([^"]+)"`)

// extractContentURL parses Reddit's Atom <content> HTML and returns the media
// URL. Reddit's content template always puts links in this order:
//
//	[0] permalink  [1] /u/author  [2] media URL  [3] permalink
func extractContentURL(content string) string {
	m := hrefRe.FindAllStringSubmatch(content, 4)
	if len(m) < 3 {
		return ""
	}
	return html.UnescapeString(m[2][1])
}

// vredditMP4URL converts a v.redd.it page URL to its DASH 720p MP4 fallback.
func vredditMP4URL(u string) string {
	path := strings.TrimPrefix(strings.TrimPrefix(u, "https://v.redd.it/"), "http://v.redd.it/")
	id := strings.SplitN(path, "/", 2)[0]
	if id == "" {
		return ""
	}
	return "https://v.redd.it/" + id + "/DASH_720.mp4"
}

func (c *RedditClient) parseRSSEntry(e atomEntry, imgurClientID string) (Post, bool) {
	postID := strings.TrimPrefix(e.ID, "t3_")
	if postID == e.ID || postID == "" {
		return Post{}, false
	}

	ts := e.Published
	if ts == "" {
		ts = e.Updated
	}
	var createdAt time.Time
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		createdAt = t.UTC()
	}

	mediaURL := extractContentURL(e.Content)
	if mediaURL == "" {
		return Post{}, false
	}

	items := c.rssMediaItems(mediaURL, e.Thumbnail.URL, imgurClientID)
	if len(items) == 0 {
		return Post{}, false
	}

	return Post{
		ID:           postID,
		Subreddit:    e.Category.Term,
		Title:        e.Title,
		Author:       strings.TrimPrefix(e.Author.Name, "/u/"),
		CreatedAt:    createdAt,
		Permalink:    e.Link.Href,
		MediaItems:   items,
		DiscoveredAt: time.Now().UTC(),
	}, true
}

func (c *RedditClient) rssMediaItems(mediaURL, thumbnail, imgurClientID string) []MediaItem {
	switch {
	case isDirectImageURL(mediaURL):
		mt := MediaImage
		if isGifURL(mediaURL) {
			mt = MediaGif
		}
		return []MediaItem{{Type: mt, URL: mediaURL, Thumbnail: thumbnail}}

	case strings.HasPrefix(mediaURL, "https://v.redd.it/") || strings.HasPrefix(mediaURL, "http://v.redd.it/"):
		mp4 := vredditMP4URL(mediaURL)
		if mp4 == "" {
			return nil
		}
		return []MediaItem{{Type: MediaVideo, URL: mp4, Thumbnail: thumbnail}}

	// Reddit gallery — show the preview thumbnail as a static image.
	case strings.Contains(mediaURL, "reddit.com/gallery/"):
		if thumbnail != "" {
			return []MediaItem{{Type: MediaImage, URL: thumbnail, Thumbnail: thumbnail}}
		}
		return nil

	case strings.Contains(mediaURL, "redgifs.com/watch/") || strings.Contains(mediaURL, "redgifs.com/ifr/"):
		if item := c.fetchRedGifsItem(mediaURL, thumbnail); item != nil {
			return []MediaItem{*item}
		}
		return nil

	case isImgurURL(mediaURL):
		return c.rssImgurItems(mediaURL, thumbnail, imgurClientID)

	case isDirectVideoURL(mediaURL):
		return []MediaItem{{Type: MediaVideo, URL: mediaURL, Thumbnail: thumbnail}}
	}
	return nil
}

func (c *RedditClient) rssImgurItems(mediaURL, thumbnail, imgurClientID string) []MediaItem {
	if isDirectImageURL(mediaURL) {
		mt := MediaImage
		if isGifURL(mediaURL) {
			mt = MediaGif
		}
		return []MediaItem{{Type: mt, URL: mediaURL, Thumbnail: thumbnail}}
	}

	u := strings.ToLower(mediaURL)
	albumHash := ""
	if idx := strings.Index(u, "imgur.com/a/"); idx >= 0 {
		albumHash = mediaURL[idx+len("imgur.com/a/"):]
	} else if idx := strings.Index(u, "imgur.com/gallery/"); idx >= 0 {
		albumHash = mediaURL[idx+len("imgur.com/gallery/"):]
	}
	albumHash = strings.Split(albumHash, "?")[0]
	albumHash = strings.Split(albumHash, "/")[0]

	if albumHash != "" && imgurClientID != "" {
		if items := c.fetchImgurAlbum(albumHash, imgurClientID, thumbnail); len(items) > 0 {
			return items
		}
	}

	// Single imgur page: prefer thumbnail, otherwise construct i.imgur.com URL.
	if thumbnail != "" {
		return []MediaItem{{Type: MediaImage, URL: thumbnail, Thumbnail: thumbnail}}
	}
	rest := mediaURL
	for _, pfx := range []string{"https://imgur.com/", "http://imgur.com/"} {
		rest = strings.TrimPrefix(rest, pfx)
	}
	imgurID := strings.SplitN(rest, ".", 2)[0]
	imgurID = strings.SplitN(imgurID, "?", 2)[0]
	if imgurID != "" && !strings.Contains(imgurID, "/") {
		return []MediaItem{{Type: MediaImage, URL: "https://i.imgur.com/" + imgurID + ".jpg", Thumbnail: thumbnail}}
	}
	return nil
}
