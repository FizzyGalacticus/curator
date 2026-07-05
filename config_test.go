package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// addList is a test helper that creates a list with just a name and
// subreddits, failing the test on error.
func addList(t *testing.T, c *Config, name string, subreddits []string) CurationList {
	t.Helper()
	list, err := c.AddList(NewListInput{Name: name, Subreddits: subreddits})
	if err != nil {
		t.Fatalf("AddList(%q): %v", name, err)
	}
	return list
}

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.CheckInterval != "30m" {
		t.Errorf("CheckInterval: want 30m, got %s", c.CheckInterval)
	}
	if c.APIPort != 8080 {
		t.Errorf("APIPort: want 8080, got %d", c.APIPort)
	}
	if c.MaxPostAgeDays != 30 {
		t.Errorf("MaxPostAgeDays: want 30, got %d", c.MaxPostAgeDays)
	}
	if c.Lists == nil {
		t.Error("Lists should not be nil")
	}
	if len(c.Lists) != 0 {
		t.Errorf("Lists should be empty, got %v", c.Lists)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	input := Config{
		Lists: []CurationList{
			{
				ID:               "l1",
				Name:             "Test List",
				Subreddits:       []string{"pics", "videos"},
				FlickrGroups:     []string{"blackandwhite"},
				LemmyCommunities: []string{"pics@lemmy.world"},
			},
		},
		CheckInterval:  "1h",
		DownloadDir:    "/custom/downloads",
		APIPort:        9000,
		MaxPostAgeDays: 7,
		ImgurClientID:  "testclientid",
		FlickrAPIKey:   "testflickrkey",
	}
	data, _ := json.Marshal(&input)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.CheckInterval != "1h" {
		t.Errorf("CheckInterval: want 1h, got %s", c.CheckInterval)
	}
	if c.APIPort != 9000 {
		t.Errorf("APIPort: want 9000, got %d", c.APIPort)
	}
	if len(c.Lists) != 1 || len(c.Lists[0].Subreddits) != 2 {
		t.Errorf("Lists: want 1 list with 2 subreddits, got %v", c.Lists)
	}
	if len(c.Lists[0].FlickrGroups) != 1 || c.Lists[0].FlickrGroups[0] != "blackandwhite" {
		t.Errorf("FlickrGroups: want [blackandwhite], got %v", c.Lists[0].FlickrGroups)
	}
	if len(c.Lists[0].LemmyCommunities) != 1 || c.Lists[0].LemmyCommunities[0] != "pics@lemmy.world" {
		t.Errorf("LemmyCommunities: want [pics@lemmy.world], got %v", c.Lists[0].LemmyCommunities)
	}
	if c.ImgurClientID != "testclientid" {
		t.Errorf("ImgurClientID: want testclientid, got %s", c.ImgurClientID)
	}
	if c.FlickrAPIKey != "testflickrkey" {
		t.Errorf("FlickrAPIKey: want testflickrkey, got %s", c.FlickrAPIKey)
	}
}

func TestLoadConfig_AppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	// Write a config with zero values for defaulted fields.
	os.WriteFile(path, []byte(`{}`), 0644)

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.CheckInterval != "30m" {
		t.Errorf("CheckInterval default: want 30m, got %s", c.CheckInterval)
	}
	if c.APIPort != 8080 {
		t.Errorf("APIPort default: want 8080, got %d", c.APIPort)
	}
	if c.MaxPostAgeDays != 30 {
		t.Errorf("MaxPostAgeDays default: want 30, got %d", c.MaxPostAgeDays)
	}
}

func TestLoadConfig_AppliesDefaults_ListMissingFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, []byte(`{"lists":[{"id":"l1","name":"x"}]}`), 0644)

	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(c.Lists) != 1 {
		t.Fatalf("want 1 list, got %d", len(c.Lists))
	}
	if c.Lists[0].Subreddits == nil || len(c.Lists[0].Subreddits) != 0 {
		t.Errorf("Subreddits should default to an empty slice, got %v", c.Lists[0].Subreddits)
	}
	if c.Lists[0].FlickrGroups == nil || len(c.Lists[0].FlickrGroups) != 0 {
		t.Errorf("FlickrGroups should default to an empty slice, got %v", c.Lists[0].FlickrGroups)
	}
	if c.Lists[0].LemmyCommunities == nil || len(c.Lists[0].LemmyCommunities) != 0 {
		t.Errorf("LemmyCommunities should default to an empty slice, got %v", c.Lists[0].LemmyCommunities)
	}
}

func TestConfig_Save_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	c := DefaultConfig()
	c.CheckInterval = "45m"
	c.Lists = []CurationList{{ID: "l1", Name: "test", Subreddits: []string{"earthporn"}}}
	c.ImgurClientID = "abc"
	c.FlickrAPIKey = "xyz"

	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	c2, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c2.CheckInterval != "45m" {
		t.Errorf("CheckInterval: want 45m, got %s", c2.CheckInterval)
	}
	if len(c2.Lists) != 1 || len(c2.Lists[0].Subreddits) != 1 || c2.Lists[0].Subreddits[0] != "earthporn" {
		t.Errorf("Lists: want [{earthporn}], got %v", c2.Lists)
	}
	if c2.ImgurClientID != "abc" {
		t.Errorf("ImgurClientID: want abc, got %s", c2.ImgurClientID)
	}
	if c2.FlickrAPIKey != "xyz" {
		t.Errorf("FlickrAPIKey: want xyz, got %s", c2.FlickrAPIKey)
	}
}

func TestConfig_AddList(t *testing.T) {
	c := DefaultConfig()

	list := addList(t, c, "SFW", []string{"pics", "wallpapers"})
	if list.ID == "" {
		t.Error("expected a generated ID")
	}
	if list.Name != "SFW" {
		t.Errorf("Name: want SFW, got %s", list.Name)
	}
	if len(list.Subreddits) != 2 {
		t.Errorf("Subreddits: want 2, got %v", list.Subreddits)
	}
	if len(c.Lists) != 1 {
		t.Fatalf("want 1 list in config, got %d", len(c.Lists))
	}

	// Empty name rejected.
	if _, err := c.AddList(NewListInput{Name: "  "}); err == nil {
		t.Error("expected error for empty name")
	}

	// Second list with an overlapping subreddit is allowed (independent lists).
	list2 := addList(t, c, "Also pics", []string{"pics"})
	if list2.ID == list.ID {
		t.Error("expected distinct generated IDs")
	}
}

func TestConfig_AddList_WithFlickrGroupsAndLemmyCommunities(t *testing.T) {
	c := DefaultConfig()

	list, err := c.AddList(NewListInput{
		Name:             "Mixed",
		Subreddits:       []string{"Pics", "pics"},                                                       // dup after lowercasing
		FlickrGroups:     []string{"https://www.flickr.com/groups/BlackAndWhite/pool/", "BlackAndWhite"}, // dup, case preserved
		LemmyCommunities: []string{"!Pics@Lemmy.World", "pics@lemmy.world"},                              // dup after normalizing
	})
	if err != nil {
		t.Fatalf("AddList: %v", err)
	}

	if len(list.Subreddits) != 1 || list.Subreddits[0] != "pics" {
		t.Errorf("Subreddits: want [pics], got %v", list.Subreddits)
	}
	if len(list.FlickrGroups) != 1 || list.FlickrGroups[0] != "BlackAndWhite" {
		t.Errorf("FlickrGroups: want [BlackAndWhite] (case preserved, deduped), got %v", list.FlickrGroups)
	}
	if len(list.LemmyCommunities) != 1 || list.LemmyCommunities[0] != "pics@lemmy.world" {
		t.Errorf("LemmyCommunities: want [pics@lemmy.world], got %v", list.LemmyCommunities)
	}
}

func TestConfig_RemoveList(t *testing.T) {
	c := DefaultConfig()
	list := addList(t, c, "A", nil)
	addList(t, c, "B", nil)

	if removed := c.RemoveList(list.ID); !removed {
		t.Error("want true for existing list")
	}
	if len(c.Lists) != 1 {
		t.Errorf("want 1 list after removal, got %d", len(c.Lists))
	}

	if removed := c.RemoveList("nothere"); removed {
		t.Error("want false for nonexistent list")
	}
}

func TestConfig_RenameList(t *testing.T) {
	c := DefaultConfig()
	list := addList(t, c, "Old Name", nil)

	if !c.RenameList(list.ID, "New Name") {
		t.Error("want true for existing list")
	}
	got, _ := c.GetList(list.ID)
	if got.Name != "New Name" {
		t.Errorf("Name: want New Name, got %s", got.Name)
	}

	if c.RenameList(list.ID, "  ") {
		t.Error("want false for empty new name")
	}
	if c.RenameList("nothere", "X") {
		t.Error("want false for nonexistent list")
	}
}

func TestConfig_AddSubredditToList(t *testing.T) {
	c := DefaultConfig()
	list := addList(t, c, "A", nil)
	other := addList(t, c, "B", nil)

	added, ok := c.AddSubredditToList(list.ID, "pics")
	if !ok || !added {
		t.Error("first add should succeed")
	}

	// Duplicate within the same list rejected.
	added, ok = c.AddSubredditToList(list.ID, "pics")
	if !ok || added {
		t.Error("duplicate add should return added=false")
	}

	// Same subreddit name allowed in a different list.
	added, ok = c.AddSubredditToList(other.ID, "pics")
	if !ok || !added {
		t.Error("same subreddit in a different list should be allowed")
	}

	// Unknown list.
	if _, ok := c.AddSubredditToList("nothere", "videos"); ok {
		t.Error("want ok=false for unknown list")
	}
}

func TestConfig_RemoveSubredditFromList(t *testing.T) {
	c := DefaultConfig()
	list := addList(t, c, "A", []string{"pics", "videos", "gifs"})

	removed, ok := c.RemoveSubredditFromList(list.ID, "videos")
	if !ok || !removed {
		t.Error("want removed=true, ok=true for existing subreddit")
	}
	got, _ := c.GetList(list.ID)
	if len(got.Subreddits) != 2 {
		t.Errorf("want 2 subreddits after removal, got %v", got.Subreddits)
	}

	removed, ok = c.RemoveSubredditFromList(list.ID, "nothere")
	if !ok || removed {
		t.Error("want removed=false, ok=true for nonexistent subreddit in existing list")
	}

	if _, ok := c.RemoveSubredditFromList("nothere", "pics"); ok {
		t.Error("want ok=false for unknown list")
	}
}

func TestConfig_AddFlickrGroupToList(t *testing.T) {
	c := DefaultConfig()
	list := addList(t, c, "A", nil)
	other := addList(t, c, "B", nil)

	added, ok := c.AddFlickrGroupToList(list.ID, "blackandwhite")
	if !ok || !added {
		t.Error("first add should succeed")
	}

	added, ok = c.AddFlickrGroupToList(list.ID, "blackandwhite")
	if !ok || added {
		t.Error("duplicate add should return added=false")
	}

	added, ok = c.AddFlickrGroupToList(other.ID, "blackandwhite")
	if !ok || !added {
		t.Error("same group in a different list should be allowed")
	}

	if _, ok := c.AddFlickrGroupToList("nothere", "x"); ok {
		t.Error("want ok=false for unknown list")
	}
}

func TestConfig_RemoveFlickrGroupFromList(t *testing.T) {
	c := DefaultConfig()
	list := addList(t, c, "A", nil)
	c.AddFlickrGroupToList(list.ID, "blackandwhite")

	removed, ok := c.RemoveFlickrGroupFromList(list.ID, "blackandwhite")
	if !ok || !removed {
		t.Error("want removed=true, ok=true for existing group")
	}
	got, _ := c.GetList(list.ID)
	if len(got.FlickrGroups) != 0 {
		t.Errorf("want 0 groups after removal, got %v", got.FlickrGroups)
	}

	removed, ok = c.RemoveFlickrGroupFromList(list.ID, "nothere")
	if !ok || removed {
		t.Error("want removed=false, ok=true for nonexistent group in existing list")
	}

	if _, ok := c.RemoveFlickrGroupFromList("nothere", "x"); ok {
		t.Error("want ok=false for unknown list")
	}
}

func TestConfig_AddLemmyCommunityToList(t *testing.T) {
	c := DefaultConfig()
	list := addList(t, c, "A", nil)
	other := addList(t, c, "B", nil)

	added, ok := c.AddLemmyCommunityToList(list.ID, "pics@lemmy.world")
	if !ok || !added {
		t.Error("first add should succeed")
	}

	added, ok = c.AddLemmyCommunityToList(list.ID, "pics@lemmy.world")
	if !ok || added {
		t.Error("duplicate add should return added=false")
	}

	added, ok = c.AddLemmyCommunityToList(other.ID, "pics@lemmy.world")
	if !ok || !added {
		t.Error("same community in a different list should be allowed")
	}

	if _, ok := c.AddLemmyCommunityToList("nothere", "x@y"); ok {
		t.Error("want ok=false for unknown list")
	}
}

func TestConfig_RemoveLemmyCommunityFromList(t *testing.T) {
	c := DefaultConfig()
	list := addList(t, c, "A", nil)
	c.AddLemmyCommunityToList(list.ID, "pics@lemmy.world")

	removed, ok := c.RemoveLemmyCommunityFromList(list.ID, "pics@lemmy.world")
	if !ok || !removed {
		t.Error("want removed=true, ok=true for existing community")
	}
	got, _ := c.GetList(list.ID)
	if len(got.LemmyCommunities) != 0 {
		t.Errorf("want 0 communities after removal, got %v", got.LemmyCommunities)
	}

	removed, ok = c.RemoveLemmyCommunityFromList(list.ID, "nothere@x")
	if !ok || removed {
		t.Error("want removed=false, ok=true for nonexistent community in existing list")
	}

	if _, ok := c.RemoveLemmyCommunityFromList("nothere", "x@y"); ok {
		t.Error("want ok=false for unknown list")
	}
}

func TestConfig_GetList_NotFound(t *testing.T) {
	c := DefaultConfig()
	if _, ok := c.GetList("nothere"); ok {
		t.Error("want ok=false for unknown list")
	}
}

func TestConfig_GetLists_ReturnsDeepCopy(t *testing.T) {
	c := DefaultConfig()
	addList(t, c, "A", []string{"a", "b"})
	c.AddFlickrGroupToList(c.Lists[0].ID, "g1")
	c.AddLemmyCommunityToList(c.Lists[0].ID, "c1@instance")

	got := c.GetLists()
	got[0].Subreddits[0] = "mutated"
	got[0].FlickrGroups[0] = "mutated"
	got[0].LemmyCommunities[0] = "mutated"

	if c.Lists[0].Subreddits[0] != "a" {
		t.Error("GetLists should return a deep copy; mutating the returned Subreddits slice should not affect Config")
	}
	if c.Lists[0].FlickrGroups[0] != "g1" {
		t.Error("GetLists should return a deep copy; mutating the returned FlickrGroups slice should not affect Config")
	}
	if c.Lists[0].LemmyCommunities[0] != "c1@instance" {
		t.Error("GetLists should return a deep copy; mutating the returned LemmyCommunities slice should not affect Config")
	}
}

func TestConfig_GetCheckInterval(t *testing.T) {
	tests := []struct {
		interval string
		want     time.Duration
	}{
		{"30m", 30 * time.Minute},
		{"1h", time.Hour},
		{"2h30m", 2*time.Hour + 30*time.Minute},
		{"invalid", 30 * time.Minute}, // fallback
		{"", 30 * time.Minute},        // fallback
		{"-5m", 30 * time.Minute},     // negative → fallback
	}
	for _, tc := range tests {
		c := DefaultConfig()
		c.CheckInterval = tc.interval
		got := c.GetCheckInterval()
		if got != tc.want {
			t.Errorf("interval=%q: want %v, got %v", tc.interval, tc.want, got)
		}
	}
}

func TestNormalizeFlickrGroupSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"blackandwhite", "blackandwhite"},
		{"  blackandwhite  ", "blackandwhite"},
		{"https://www.flickr.com/groups/blackandwhite/", "blackandwhite"},
		{"https://www.flickr.com/groups/blackandwhite/pool/", "blackandwhite"},
		{"http://flickr.com/groups/blackandwhite", "blackandwhite"},
		{"BlackAndWhite", "BlackAndWhite"}, // case preserved, unlike subreddits
	}
	for _, tc := range tests {
		if got := normalizeFlickrGroupSlug(tc.input); got != tc.want {
			t.Errorf("normalizeFlickrGroupSlug(%q): want %q, got %q", tc.input, tc.want, got)
		}
	}
}

func TestNormalizeLemmyIdentifier(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"pics@lemmy.world", "pics@lemmy.world"},
		{"!pics@lemmy.world", "pics@lemmy.world"},
		{"Pics@Lemmy.World", "pics@lemmy.world"}, // lowercased, unlike Flickr slugs
		{"noatsign", ""},
		{"@nocommunity", ""},
		{"comm@", ""},
	}
	for _, tc := range tests {
		if got := normalizeLemmyIdentifier(tc.input); got != tc.want {
			t.Errorf("normalizeLemmyIdentifier(%q): want %q, got %q", tc.input, tc.want, got)
		}
	}
}
