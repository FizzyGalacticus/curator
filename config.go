package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

var errEmptyListName = errors.New("list name is required")

// CurationList is a named, independently-scheduled set of source identifiers.
// Subreddits, FlickrGroups, and LemmyCommunities are all queried and merged
// into the same list's post pool — they're additive sources, not fallback
// alternatives to each other.
type CurationList struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Subreddits       []string `json:"subreddits"`
	FlickrGroups     []string `json:"flickr_groups"`
	LemmyCommunities []string `json:"lemmy_communities"` // "community@instance" strings
}

// NewListInput describes the fields accepted when creating a curation list.
type NewListInput struct {
	Name             string
	Subreddits       []string
	FlickrGroups     []string
	LemmyCommunities []string
}

// Config holds application configuration, persisted to config.json.
type Config struct {
	sync.RWMutex
	Lists          []CurationList `json:"lists"`
	CheckInterval  string         `json:"check_interval"` // Go duration string, e.g. "30m"
	DownloadDir    string         `json:"download_dir"`
	APIPort        int            `json:"api_port"`
	MaxPostAgeDays int            `json:"max_post_age_days"` // 0 means no pruning
	// ImgurClientID enables Imgur album expansion. Get a free client_id at
	// https://api.imgur.com/oauth2/addclient (no approval needed, anonymous usage).
	ImgurClientID string `json:"imgur_client_id,omitempty"`
	// FlickrAPIKey enables the Flickr group-pool source. Get a free key at
	// https://www.flickr.com/services/apps/create/.
	FlickrAPIKey string `json:"flickr_api_key,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Lists:          []CurationList{},
		CheckInterval:  "30m",
		DownloadDir:    "/app/downloads",
		APIPort:        8080,
		MaxPostAgeDays: 30,
	}
}

// LoadConfig reads and parses a config JSON file.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	applyConfigDefaults(&c)
	return &c, nil
}

// Save writes the config to the given path as indented JSON.
func (c *Config) Save(path string) error {
	c.RLock()
	defer c.RUnlock()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ReloadFromDisk re-reads the config file and updates in-memory state.
func (c *Config) ReloadFromDisk(path string) error {
	c.Lock()
	defer c.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return err
	}
	applyConfigDefaults(c)
	return nil
}

// GetCheckInterval returns the check interval as a duration, with a fallback.
func (c *Config) GetCheckInterval() time.Duration {
	c.RLock()
	defer c.RUnlock()
	d, err := time.ParseDuration(c.CheckInterval)
	if err != nil || d <= 0 {
		return 30 * time.Minute
	}
	return d
}

// GetLists returns a deep copy of all curation lists.
func (c *Config) GetLists() []CurationList {
	c.RLock()
	defer c.RUnlock()
	out := make([]CurationList, len(c.Lists))
	for i, l := range c.Lists {
		out[i] = copyList(l)
	}
	return out
}

// GetList returns a deep copy of the curation list with the given ID.
func (c *Config) GetList(id string) (CurationList, bool) {
	c.RLock()
	defer c.RUnlock()
	for _, l := range c.Lists {
		if l.ID == id {
			return copyList(l), true
		}
	}
	return CurationList{}, false
}

// AddList creates a new curation list with a generated ID.
func (c *Config) AddList(input NewListInput) (CurationList, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return CurationList{}, errEmptyListName
	}

	list := CurationList{
		Name:             name,
		Subreddits:       normalizeSubreddits(input.Subreddits),
		FlickrGroups:     normalizeFlickrGroupSlugs(input.FlickrGroups),
		LemmyCommunities: normalizeLemmyIdentifiers(input.LemmyCommunities),
	}

	c.Lock()
	defer c.Unlock()

	list.ID = c.generateListID()
	c.Lists = append(c.Lists, list)
	return copyList(list), nil
}

// RemoveList deletes a curation list by ID.
func (c *Config) RemoveList(id string) bool {
	c.Lock()
	defer c.Unlock()
	for i, l := range c.Lists {
		if l.ID == id {
			c.Lists = append(c.Lists[:i], c.Lists[i+1:]...)
			return true
		}
	}
	return false
}

// RenameList updates a curation list's name.
func (c *Config) RenameList(id, newName string) bool {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return false
	}
	c.Lock()
	defer c.Unlock()
	for i, l := range c.Lists {
		if l.ID == id {
			c.Lists[i].Name = newName
			return true
		}
	}
	return false
}

// addToListField adds name to the slice selected by field on the list with
// the given ID, skipping it if already present.
// ok is false if the list doesn't exist; added is false if name was already present.
func (c *Config) addToListField(listID, name string, field func(*CurationList) *[]string) (added bool, ok bool) {
	c.Lock()
	defer c.Unlock()
	for i := range c.Lists {
		if c.Lists[i].ID != listID {
			continue
		}
		slicePtr := field(&c.Lists[i])
		for _, s := range *slicePtr {
			if s == name {
				return false, true
			}
		}
		*slicePtr = append(*slicePtr, name)
		return true, true
	}
	return false, false
}

// removeFromListField removes name from the slice selected by field on the
// list with the given ID.
// ok is false if the list doesn't exist; removed is false if name wasn't present.
func (c *Config) removeFromListField(listID, name string, field func(*CurationList) *[]string) (removed bool, ok bool) {
	c.Lock()
	defer c.Unlock()
	for i := range c.Lists {
		if c.Lists[i].ID != listID {
			continue
		}
		slicePtr := field(&c.Lists[i])
		for j, s := range *slicePtr {
			if s == name {
				*slicePtr = append((*slicePtr)[:j], (*slicePtr)[j+1:]...)
				return true, true
			}
		}
		return false, true
	}
	return false, false
}

func subredditsField(l *CurationList) *[]string       { return &l.Subreddits }
func flickrGroupsField(l *CurationList) *[]string     { return &l.FlickrGroups }
func lemmyCommunitiesField(l *CurationList) *[]string { return &l.LemmyCommunities }

// AddSubredditToList adds a subreddit to a list if not already present.
func (c *Config) AddSubredditToList(listID, name string) (added bool, ok bool) {
	return c.addToListField(listID, name, subredditsField)
}

// RemoveSubredditFromList removes a subreddit from a list by name.
func (c *Config) RemoveSubredditFromList(listID, name string) (removed bool, ok bool) {
	return c.removeFromListField(listID, name, subredditsField)
}

// AddFlickrGroupToList adds a Flickr group slug to a list if not already present.
func (c *Config) AddFlickrGroupToList(listID, name string) (added bool, ok bool) {
	return c.addToListField(listID, name, flickrGroupsField)
}

// RemoveFlickrGroupFromList removes a Flickr group slug from a list by name.
func (c *Config) RemoveFlickrGroupFromList(listID, name string) (removed bool, ok bool) {
	return c.removeFromListField(listID, name, flickrGroupsField)
}

// AddLemmyCommunityToList adds a Lemmy "community@instance" identifier to a
// list if not already present.
func (c *Config) AddLemmyCommunityToList(listID, name string) (added bool, ok bool) {
	return c.addToListField(listID, name, lemmyCommunitiesField)
}

// RemoveLemmyCommunityFromList removes a Lemmy "community@instance" identifier
// from a list by name.
func (c *Config) RemoveLemmyCommunityFromList(listID, name string) (removed bool, ok bool) {
	return c.removeFromListField(listID, name, lemmyCommunitiesField)
}

// generateListID returns a random hex ID not already used by another list.
// Must be called with c locked.
func (c *Config) generateListID() string {
	for attempt := 0; attempt < 10; attempt++ {
		id := randomHexID()
		collision := false
		for _, l := range c.Lists {
			if l.ID == id {
				collision = true
				break
			}
		}
		if !collision {
			return id
		}
	}
	// Astronomically unlikely, but fall back to a longer ID to guarantee progress.
	return randomHexID() + randomHexID()
}

func randomHexID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is effectively unrecoverable; fall back to time-based
		// uniqueness rather than panicking.
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return hex.EncodeToString(b)
}

func normalizeSubreddits(subreddits []string) []string {
	out := make([]string, 0, len(subreddits))
	seen := map[string]bool{}
	for _, s := range subreddits {
		name := strings.ToLower(strings.TrimSpace(s))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// normalizeFlickrGroupSlug strips a full Flickr group URL down to its slug
// (e.g. "https://www.flickr.com/groups/blackandwhite/pool/" -> "blackandwhite").
// Slugs are NOT lowercased — unlike subreddits, Flickr group URLs are case-sensitive.
func normalizeFlickrGroupSlug(input string) string {
	slug := strings.TrimSpace(input)
	for _, prefix := range []string{
		"https://www.flickr.com/groups/",
		"http://www.flickr.com/groups/",
		"https://flickr.com/groups/",
		"http://flickr.com/groups/",
	} {
		slug = strings.TrimPrefix(slug, prefix)
	}
	slug = strings.TrimSuffix(slug, "/")
	slug = strings.TrimSuffix(slug, "/pool")
	slug = strings.TrimSuffix(slug, "/")
	return slug
}

func normalizeFlickrGroupSlugs(slugs []string) []string {
	out := make([]string, 0, len(slugs))
	seen := map[string]bool{}
	for _, s := range slugs {
		slug := normalizeFlickrGroupSlug(s)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, slug)
	}
	return out
}

// normalizeLemmyIdentifier strips a leading "!" and lowercases a Lemmy
// "community@instance" identifier. Returns "" if the input doesn't contain
// exactly one "@" with non-empty community and instance parts.
func normalizeLemmyIdentifier(input string) string {
	id := strings.ToLower(strings.TrimSpace(input))
	id = strings.TrimPrefix(id, "!")
	community, instance, ok := splitLemmyIdentifier(id)
	if !ok {
		return ""
	}
	return community + "@" + instance
}

func normalizeLemmyIdentifiers(identifiers []string) []string {
	out := make([]string, 0, len(identifiers))
	seen := map[string]bool{}
	for _, s := range identifiers {
		id := normalizeLemmyIdentifier(s)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func copyList(l CurationList) CurationList {
	subs := make([]string, len(l.Subreddits))
	copy(subs, l.Subreddits)
	groups := make([]string, len(l.FlickrGroups))
	copy(groups, l.FlickrGroups)
	communities := make([]string, len(l.LemmyCommunities))
	copy(communities, l.LemmyCommunities)
	return CurationList{
		ID:               l.ID,
		Name:             l.Name,
		Subreddits:       subs,
		FlickrGroups:     groups,
		LemmyCommunities: communities,
	}
}

func applyConfigDefaults(c *Config) {
	if c.CheckInterval == "" {
		c.CheckInterval = "30m"
	}
	if c.DownloadDir == "" {
		c.DownloadDir = "/app/downloads"
	}
	if c.APIPort == 0 {
		c.APIPort = 8080
	}
	if c.MaxPostAgeDays == 0 {
		c.MaxPostAgeDays = 30
	}
	if c.Lists == nil {
		c.Lists = []CurationList{}
	}
	for i, l := range c.Lists {
		if l.Subreddits == nil {
			c.Lists[i].Subreddits = []string{}
		}
		if l.FlickrGroups == nil {
			c.Lists[i].FlickrGroups = []string{}
		}
		if l.LemmyCommunities == nil {
			c.Lists[i].LemmyCommunities = []string{}
		}
	}
}
