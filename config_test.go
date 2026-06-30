package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	if c.Subreddits == nil {
		t.Error("Subreddits should not be nil")
	}
	if len(c.Subreddits) != 0 {
		t.Errorf("Subreddits should be empty, got %v", c.Subreddits)
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
		Subreddits:     []string{"pics", "videos"},
		CheckInterval:  "1h",
		DownloadDir:    "/custom/downloads",
		APIPort:        9000,
		MaxPostAgeDays: 7,
		ImgurClientID:  "testclientid",
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
	if len(c.Subreddits) != 2 {
		t.Errorf("Subreddits: want 2, got %d", len(c.Subreddits))
	}
	if c.ImgurClientID != "testclientid" {
		t.Errorf("ImgurClientID: want testclientid, got %s", c.ImgurClientID)
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

func TestConfig_Save_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	c := DefaultConfig()
	c.CheckInterval = "45m"
	c.Subreddits = []string{"earthporn"}
	c.ImgurClientID = "abc"

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
	if len(c2.Subreddits) != 1 || c2.Subreddits[0] != "earthporn" {
		t.Errorf("Subreddits: want [earthporn], got %v", c2.Subreddits)
	}
	if c2.ImgurClientID != "abc" {
		t.Errorf("ImgurClientID: want abc, got %s", c2.ImgurClientID)
	}
}

func TestConfig_AddSubreddit(t *testing.T) {
	c := DefaultConfig()

	if added := c.AddSubreddit("pics"); !added {
		t.Error("first add should return true")
	}
	if len(c.Subreddits) != 1 {
		t.Fatalf("want 1 subreddit, got %d", len(c.Subreddits))
	}

	// Duplicate should be rejected.
	if added := c.AddSubreddit("pics"); added {
		t.Error("duplicate add should return false")
	}
	if len(c.Subreddits) != 1 {
		t.Errorf("want 1 subreddit after dup, got %d", len(c.Subreddits))
	}

	// Second unique entry.
	c.AddSubreddit("videos")
	if len(c.Subreddits) != 2 {
		t.Errorf("want 2 subreddits, got %d", len(c.Subreddits))
	}
}

func TestConfig_RemoveSubreddit(t *testing.T) {
	c := DefaultConfig()
	c.Subreddits = []string{"pics", "videos", "gifs"}

	if removed := c.RemoveSubreddit("videos"); !removed {
		t.Error("want true for existing subreddit")
	}
	if len(c.Subreddits) != 2 {
		t.Errorf("want 2 after removal, got %d", len(c.Subreddits))
	}
	for _, s := range c.Subreddits {
		if s == "videos" {
			t.Error("videos still present after removal")
		}
	}

	if removed := c.RemoveSubreddit("nothere"); removed {
		t.Error("want false for nonexistent subreddit")
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

func TestConfig_GetSubreddits_ReturnsCopy(t *testing.T) {
	c := DefaultConfig()
	c.Subreddits = []string{"a", "b"}

	got := c.GetSubreddits()
	got[0] = "mutated"

	if c.Subreddits[0] != "a" {
		t.Error("GetSubreddits should return a copy, not a reference")
	}
}
