package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// LemmyClient fetches posts from a Lemmy instance's public post-listing API.
// Identifiers are self-contained "community@instance" strings, since Lemmy is
// federated and the same community name can exist on many different instances.
type LemmyClient struct {
	http    httpDoer
	baseURL string // overridable in tests; production: "https://" + instance
}

// NewLemmyClient creates a client with sensible timeouts.
func NewLemmyClient() *LemmyClient {
	return &LemmyClient{
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

type lemmyPost struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	URL          string `json:"url,omitempty"`
	ThumbnailURL string `json:"thumbnail_url,omitempty"`
	Published    string `json:"published"`
	APID         string `json:"ap_id"`
	NSFW         bool   `json:"nsfw"`
}

type lemmyCommunity struct {
	Name string `json:"name"`
	NSFW bool   `json:"nsfw"`
}

type lemmyPostView struct {
	Post      lemmyPost      `json:"post"`
	Community lemmyCommunity `json:"community"`
}

type lemmyPostListResponse struct {
	Posts []lemmyPostView `json:"posts"`
}

// FetchNewPosts implements PostFetcher for Lemmy. identifier is a
// "community@instance" string (an optional leading "!" is accepted and
// stripped). creds is unused — Lemmy's public post-listing API needs no
// credential.
func (c *LemmyClient) FetchNewPosts(identifier string, since time.Time, _ FetchCredentials) ([]Post, error) {
	community, instance, ok := splitLemmyIdentifier(identifier)
	if !ok {
		return nil, fmt.Errorf("lemmy: invalid identifier %q, expected \"community@instance\"", identifier)
	}

	base := c.baseURL
	if base == "" {
		base = "https://" + instance
	}
	// type_ is intentionally omitted: its valid values are All|Local|Subscribed|
	// ModeratorView (NOT "Community" as might be assumed), and community_name
	// alone already scopes the query to the requested community.
	url := fmt.Sprintf("%s/api/v3/post/list?community_name=%s&sort=New&limit=50", base, community)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", redditUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lemmy: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("lemmy: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var result lemmyPostListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("lemmy: decoding response: %w", err)
	}

	var posts []Post
	for _, pv := range result.Posts {
		if pv.Post.NSFW || pv.Community.NSFW {
			continue
		}
		media := lemmyMediaItem(pv.Post)
		if media == nil {
			continue
		}
		publishedAt, err := time.Parse(time.RFC3339Nano, pv.Post.Published)
		if err != nil {
			continue
		}
		// Featured/pinned posts can appear out of chronological order even
		// with sort=New, so every post is checked against `since` rather than
		// stopping at the first one that looks old enough.
		if !since.IsZero() && !publishedAt.After(since) {
			continue
		}
		posts = append(posts, Post{
			ID:           fmt.Sprintf("lemmy_%s_%d", instance, pv.Post.ID),
			Source:       SourceLemmy,
			Subreddit:    identifier,
			Title:        pv.Post.Name,
			CreatedAt:    publishedAt,
			Permalink:    pv.Post.APID,
			MediaItems:   []MediaItem{*media},
			DiscoveredAt: time.Now().UTC(),
		})
	}

	log.Printf("lemmy: %s → %d posts", identifier, len(posts))
	return posts, nil
}

// splitLemmyIdentifier parses a "community@instance" (optionally prefixed
// with "!") identifier. Returns ok=false if the format is invalid.
func splitLemmyIdentifier(identifier string) (community, instance string, ok bool) {
	id := strings.TrimPrefix(strings.TrimSpace(identifier), "!")
	idx := strings.LastIndex(id, "@")
	if idx <= 0 || idx == len(id)-1 {
		return "", "", false
	}
	community = id[:idx]
	instance = id[idx+1:]
	if community == "" || instance == "" || strings.Contains(instance, "/") {
		return "", "", false
	}
	return community, instance, true
}

// lemmyMediaItem classifies a Lemmy post's URL as image/gif/video, reusing
// the same URL-suffix heuristics as the Reddit RSS fetcher.
func lemmyMediaItem(p lemmyPost) *MediaItem {
	if p.URL == "" {
		return nil
	}
	switch {
	case isDirectImageURL(p.URL):
		mt := MediaImage
		if isGifURL(p.URL) {
			mt = MediaGif
		}
		return &MediaItem{Type: mt, URL: p.URL, Thumbnail: p.ThumbnailURL}
	case isDirectVideoURL(p.URL):
		return &MediaItem{Type: MediaVideo, URL: p.URL, Thumbnail: p.ThumbnailURL}
	default:
		return nil
	}
}
