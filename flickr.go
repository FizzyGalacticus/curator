package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const flickrRestBase = "https://api.flickr.com/services/rest/"

// FlickrClient fetches posts from a Flickr group's photo pool. Groups are
// curated submission pools (users explicitly add photos to them), which is
// semantically closer to a subreddit than Flickr's broader tag search.
//
// NOTE: the exact field names below (dateupload, ownername, etc.) are
// best-effort based on documented Flickr REST API conventions and could not
// be verified against a live response (Flickr's API requires a registered
// key). Double-check these against a real flickr.groups.pools.getPhotos
// response the first time a real API key is available.
type FlickrClient struct {
	http     httpDoer
	baseURL  string // overridable in tests; production: flickrRestBase
	mu       sync.Mutex
	groupIDs map[string]string // group slug -> resolved numeric group NSID
}

// NewFlickrClient creates a client with sensible timeouts.
func NewFlickrClient() *FlickrClient {
	return &FlickrClient{
		http:     &http.Client{Timeout: 30 * time.Second},
		baseURL:  flickrRestBase,
		groupIDs: map[string]string{},
	}
}

type flickrLookupGroupResponse struct {
	Group struct {
		ID string `json:"id"`
	} `json:"group"`
	Stat    string `json:"stat"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// flickrPhoto is one entry from flickr.groups.pools.getPhotos, requested with
// extras=url_o,url_l,url_m,url_sq,media,date_upload,owner_name. Flickr drops
// underscores from some extras field names in the response (date_upload ->
// dateupload, owner_name -> ownername) — a known API quirk.
type flickrPhoto struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Title      string `json:"title"`
	Media      string `json:"media"` // "photo" | "video"
	DateUpload string `json:"dateupload"`
	OwnerName  string `json:"ownername"`

	URLOriginal    string `json:"url_o,omitempty"`
	WidthOriginal  string `json:"width_o,omitempty"`
	HeightOriginal string `json:"height_o,omitempty"`

	URLLarge    string `json:"url_l,omitempty"`
	WidthLarge  string `json:"width_l,omitempty"`
	HeightLarge string `json:"height_l,omitempty"`

	URLMedium    string `json:"url_m,omitempty"`
	WidthMedium  string `json:"width_m,omitempty"`
	HeightMedium string `json:"height_m,omitempty"`

	URLSquare string `json:"url_sq,omitempty"`
}

type flickrPoolPhotosResponse struct {
	Photos struct {
		Photo []flickrPhoto `json:"photo"`
	} `json:"photos"`
	Stat    string `json:"stat"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// FetchNewPosts implements PostFetcher for Flickr. group is a group URL slug
// (e.g. "blackandwhite"), already normalized by normalizeFlickrGroupSlug when
// it was added to a list. Requires creds.FlickrAPIKey.
func (c *FlickrClient) FetchNewPosts(group string, since time.Time, creds FetchCredentials) ([]Post, error) {
	if creds.FlickrAPIKey == "" {
		return nil, fmt.Errorf("flickr: no API key configured (set flickr_api_key in settings)")
	}

	groupID, err := c.resolveGroupID(group, creds.FlickrAPIKey)
	if err != nil {
		return nil, fmt.Errorf("flickr: resolving group %q: %w", group, err)
	}

	photos, err := c.fetchPoolPhotos(groupID, creds.FlickrAPIKey)
	if err != nil {
		return nil, fmt.Errorf("flickr: fetching pool %q: %w", group, err)
	}

	var posts []Post
	for _, p := range photos {
		// Flickr's public API (API-key-only, no OAuth) can't resolve playable
		// video file URLs — that requires an OAuth 1.0a-signed
		// flickr.videos.getStreams call, needing a user account + app secret.
		// Photos only, until/unless full OAuth is worth adding.
		if p.Media == "video" {
			continue
		}
		item := flickrPhotoMediaItem(p)
		if item == nil {
			continue
		}
		uploadedAt := parseFlickrUnixTime(p.DateUpload)
		// getPhotos has no server-side date filter or guaranteed sort order,
		// so every photo is checked against `since` rather than assuming it's
		// safe to stop at the first one that looks old enough.
		if !since.IsZero() && !uploadedAt.IsZero() && !uploadedAt.After(since) {
			continue
		}
		posts = append(posts, Post{
			ID:           "flickr_" + p.ID,
			Source:       SourceFlickr,
			Subreddit:    group,
			Title:        p.Title,
			Author:       p.OwnerName,
			CreatedAt:    uploadedAt,
			Permalink:    fmt.Sprintf("https://www.flickr.com/photos/%s/%s/", p.Owner, p.ID),
			MediaItems:   []MediaItem{*item},
			DiscoveredAt: time.Now().UTC(),
		})
	}

	log.Printf("flickr: group %s → %d photos", group, len(posts))
	return posts, nil
}

// resolveGroupID resolves a group's URL slug to its numeric NSID, caching the
// result in memory since resolution is stable and shouldn't re-run every pass.
func (c *FlickrClient) resolveGroupID(slug, apiKey string) (string, error) {
	c.mu.Lock()
	if id, ok := c.groupIDs[slug]; ok {
		c.mu.Unlock()
		return id, nil
	}
	c.mu.Unlock()

	params := url.Values{
		"method":         {"flickr.urls.lookupGroup"},
		"api_key":        {apiKey},
		"url":            {"https://www.flickr.com/groups/" + slug + "/"},
		"format":         {"json"},
		"nojsoncallback": {"1"},
	}
	var result flickrLookupGroupResponse
	if err := c.getJSON(params, &result); err != nil {
		return "", err
	}
	if result.Stat != "ok" {
		return "", fmt.Errorf("lookupGroup %q: %s (code %d)", slug, result.Message, result.Code)
	}

	c.mu.Lock()
	c.groupIDs[slug] = result.Group.ID
	c.mu.Unlock()
	return result.Group.ID, nil
}

func (c *FlickrClient) fetchPoolPhotos(groupID, apiKey string) ([]flickrPhoto, error) {
	params := url.Values{
		"method":         {"flickr.groups.pools.getPhotos"},
		"api_key":        {apiKey},
		"group_id":       {groupID},
		"extras":         {"url_o,url_l,url_m,url_sq,media,date_upload,owner_name"},
		"per_page":       {"100"},
		"format":         {"json"},
		"nojsoncallback": {"1"},
	}
	var result flickrPoolPhotosResponse
	if err := c.getJSON(params, &result); err != nil {
		return nil, err
	}
	if result.Stat != "ok" {
		return nil, fmt.Errorf("pools.getPhotos %q: %s (code %d)", groupID, result.Message, result.Code)
	}
	return result.Photos.Photo, nil
}

func (c *FlickrClient) getJSON(params url.Values, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", redditUserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// flickrPhotoMediaItem prefers the original size, falling back to large then
// medium, with the square thumbnail for the grid.
func flickrPhotoMediaItem(p flickrPhoto) *MediaItem {
	photoURL, width, height := p.URLOriginal, p.WidthOriginal, p.HeightOriginal
	if photoURL == "" {
		photoURL, width, height = p.URLLarge, p.WidthLarge, p.HeightLarge
	}
	if photoURL == "" {
		photoURL, width, height = p.URLMedium, p.WidthMedium, p.HeightMedium
	}
	if photoURL == "" {
		return nil
	}

	mediaType := MediaImage
	if isGifURL(photoURL) {
		mediaType = MediaGif
	}

	w, _ := strconv.Atoi(width)
	h, _ := strconv.Atoi(height)
	return &MediaItem{
		Type:      mediaType,
		URL:       photoURL,
		Thumbnail: p.URLSquare,
		Width:     w,
		Height:    h,
	}
}

func parseFlickrUnixTime(s string) time.Time {
	sec, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}
