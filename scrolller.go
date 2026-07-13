package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strings"
	"time"
)

const scrolllerAPI = "https://api.scrolller.com/admin"

const scrolllerQuery = `
query SubredditQuery(
  $url: String!
  $iterator: String
  $filter: GalleryFilter
  $sortBy: GallerySortBy
  $limit: Int!
) {
  getSubreddit(data: {url: $url, iterator: $iterator, filter: $filter, limit: $limit, sortBy: $sortBy}) {
    id
    url
    title
    isNsfw
    children {
      iterator
      items {
        __typename
        id
        url
        title
        mediaSources {
          url
          width
          height
        }
        fullLengthSource
        redgifsSource
        gfycatSource
      }
    }
  }
}`

// ScrolllerClient fetches posts from Scrolller's GraphQL API.
type ScrolllerClient struct {
	http    httpDoer
	baseURL string // overridable in tests
}

// NewScrolllerClient creates a client with sensible timeouts.
func NewScrolllerClient() *ScrolllerClient {
	return &ScrolllerClient{
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: scrolllerAPI,
	}
}

// scrolllerMediaSource is one entry in a post's mediaSources array.
type scrolllerMediaSource struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// scrolllerPost is one item from getSubreddit.children.items.
type scrolllerPost struct {
	Typename      string                 `json:"__typename"`
	ID            int                    `json:"id"`
	URL           string                 `json:"url"`
	Title         string                 `json:"title"`
	MediaSources  []scrolllerMediaSource `json:"mediaSources"`
	FullLength    *string                `json:"fullLengthSource"`
	RedgifsSource *string                `json:"redgifsSource"`
	GfycatSource  *string                `json:"gfycatSource"`
}

type scrolllerChildren struct {
	Iterator *string         `json:"iterator"`
	Items    []scrolllerPost `json:"items"`
}

type scrolllerSubreddit struct {
	ID       int               `json:"id"`
	URL      string            `json:"url"`
	Title    string            `json:"title"`
	IsNsfw   bool              `json:"isNsfw"`
	Children scrolllerChildren `json:"children"`
}

type scrolllerResponse struct {
	Data struct {
		GetSubreddit *scrolllerSubreddit `json:"getSubreddit"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// FetchNewPosts implements PostFetcher for Scrolller. Posts come from the
// subreddit's HOT ranking, so `since` is only used to spread synthetic
// timestamps — repeats across passes are deduplicated by ID in storage.
// The creds parameter is unused but required by the interface.
func (c *ScrolllerClient) FetchNewPosts(subreddit string, since time.Time, _ FetchCredentials) ([]Post, error) {
	url := "/r/" + subreddit

	body, err := json.Marshal(map[string]any{
		"query": scrolllerQuery,
		"variables": map[string]any{
			"url":      url,
			"iterator": nil,
			"filter":   nil,
			"sortBy":   "HOT",
			"limit":    50,
		},
		"authorization": nil,
	})
	if err != nil {
		return nil, fmt.Errorf("scrolller: marshaling request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/115.0")
	req.Header.Set("Origin", "https://scrolller.com")
	req.Header.Set("Referer", "https://scrolller.com"+url)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrolller: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("scrolller: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var result scrolllerResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("scrolller: decoding response: %w", err)
	}

	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("scrolller: GraphQL error: %s", result.Errors[0].Message)
	}

	if result.Data.GetSubreddit == nil {
		return nil, fmt.Errorf("scrolller: subreddit /r/%s not found", subreddit)
	}

	sub := result.Data.GetSubreddit
	log.Printf("scrolller: r/%s → %d posts", subreddit, len(sub.Children.Items))

	now := time.Now().UTC()
	var posts []Post
	for i, item := range sub.Children.Items {
		media := scrolllerMediaItem(item)
		if media == nil {
			continue
		}
		post := Post{
			ID:        fmt.Sprintf("scrolller_%d", item.ID),
			Source:    SourceReddit,
			Subreddit: subreddit,
			Title:     item.Title,
			Author:    "",
			Score:     0,
			// Scrolller doesn't expose the real post time, so a synthetic
			// timestamp is spread across the window since this subreddit was
			// last checked (item 0 = hottest = now) rather than stamping every
			// item with the same fetch instant. Otherwise every post from one
			// subreddit's batch lands within milliseconds of each other and
			// sorts as one solid block instead of interleaving with posts
			// from other subreddits/sources.
			CreatedAt:    syntheticScrolllerTimestamp(i, len(sub.Children.Items), since, now),
			Permalink:    "https://scrolller.com" + item.URL,
			MediaItems:   []MediaItem{*media},
			DiscoveredAt: now,
		}
		// Scrolller doesn't provide real timestamps, so we can't filter by
		// since. Accept all posts and let the dedup in storage handle repeated fetches.
		posts = append(posts, post)
	}

	return posts, nil
}

// syntheticScrolllerTimestamp approximates a post's creation time since
// Scrolller doesn't expose one. Items arrive in HOT-rank order, so rank is
// used as a recency proxy: posts are spread linearly across the time since
// the subreddit was last checked (hottest = now), falling back to a
// 30-minute window on the first-ever check.
func syntheticScrolllerTimestamp(index, total int, since, now time.Time) time.Time {
	if total <= 1 {
		return now
	}
	window := 30 * time.Minute
	if !since.IsZero() && now.After(since) {
		window = now.Sub(since)
	}
	frac := float64(index) / float64(total-1)
	return now.Add(-time.Duration(frac * float64(window)))
}

// scrolllerMediaItem picks the best media URL from a Scrolller post.
// Returns nil if no usable media is found.
func scrolllerMediaItem(p scrolllerPost) *MediaItem {
	if len(p.MediaSources) == 0 {
		return nil
	}

	// Separate video/webm sources from image sources.
	var videoSrcs, imgSrcs []scrolllerMediaSource
	for _, s := range p.MediaSources {
		ext := strings.ToLower(path.Ext(s.URL))
		switch ext {
		case ".mp4", ".webm":
			videoSrcs = append(videoSrcs, s)
		case ".jpg", ".jpeg", ".png", ".webp", ".gif":
			imgSrcs = append(imgSrcs, s)
		}
	}

	if len(videoSrcs) > 0 {
		// Pick the highest-resolution mp4 (prefer mp4 over webm for compatibility).
		best := pickBestSource(videoSrcs, ".mp4")
		if best == nil {
			best = &videoSrcs[0]
		}
		thumb := pickSmallestSource(imgSrcs)
		item := &MediaItem{
			Type:   MediaVideo,
			URL:    best.URL,
			Width:  best.Width,
			Height: best.Height,
		}
		if thumb != nil {
			item.Thumbnail = thumb.URL
		}
		return item
	}

	if len(imgSrcs) == 0 {
		return nil
	}

	// Pick the highest-resolution jpg (prefer jpg over webp for compatibility).
	best := pickBestSource(imgSrcs, ".jpg", ".jpeg", ".png")
	if best == nil {
		best = pickBestSource(imgSrcs) // any extension
	}
	thumb := pickSmallestWebp(imgSrcs)

	item := &MediaItem{
		Type:   mediaTypeFromExt(path.Ext(best.URL)),
		URL:    best.URL,
		Width:  best.Width,
		Height: best.Height,
	}
	if thumb != nil && thumb.URL != best.URL {
		item.Thumbnail = thumb.URL
	}
	return item
}

// pickBestSource returns the largest source (by area) among those matching the preferred exts.
// If no preferred-ext sources exist, returns the largest among all.
func pickBestSource(srcs []scrolllerMediaSource, preferExts ...string) *scrolllerMediaSource {
	var candidates []scrolllerMediaSource
	if len(preferExts) > 0 {
		for _, s := range srcs {
			ext := strings.ToLower(path.Ext(s.URL))
			for _, pe := range preferExts {
				if ext == pe {
					candidates = append(candidates, s)
					break
				}
			}
		}
	}
	if len(candidates) == 0 {
		candidates = srcs
	}

	var best *scrolllerMediaSource
	for i := range candidates {
		if best == nil || candidates[i].Width*candidates[i].Height > best.Width*best.Height {
			best = &candidates[i]
		}
	}
	return best
}

// pickSmallestSource returns the smallest source by area.
func pickSmallestSource(srcs []scrolllerMediaSource) *scrolllerMediaSource {
	if len(srcs) == 0 {
		return nil
	}
	best := &srcs[0]
	for i := range srcs {
		if srcs[i].Width*srcs[i].Height < best.Width*best.Height {
			best = &srcs[i]
		}
	}
	return best
}

// pickSmallestWebp prefers the smallest webp for thumbnails.
func pickSmallestWebp(srcs []scrolllerMediaSource) *scrolllerMediaSource {
	var webps []scrolllerMediaSource
	for _, s := range srcs {
		if strings.ToLower(path.Ext(s.URL)) == ".webp" {
			webps = append(webps, s)
		}
	}
	if len(webps) > 0 {
		return pickSmallestSource(webps)
	}
	return pickSmallestSource(srcs)
}

func mediaTypeFromExt(ext string) MediaType {
	switch strings.ToLower(ext) {
	case ".gif":
		return MediaGif
	case ".mp4", ".webm":
		return MediaVideo
	default:
		return MediaImage
	}
}
