package main

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Config holds application configuration, persisted to config.json.
type Config struct {
	sync.RWMutex
	Subreddits     []string `json:"subreddits"`
	CheckInterval  string   `json:"check_interval"`    // Go duration string, e.g. "30m"
	DownloadDir    string   `json:"download_dir"`
	APIPort        int      `json:"api_port"`
	MaxPostAgeDays int      `json:"max_post_age_days"` // 0 means no pruning
	// ImgurClientID enables Imgur album expansion. Get a free client_id at
	// https://api.imgur.com/oauth2/addclient (no approval needed, anonymous usage).
	ImgurClientID string `json:"imgur_client_id,omitempty"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Subreddits:     []string{},
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

// GetSubreddits returns a copy of the subreddit list.
func (c *Config) GetSubreddits() []string {
	c.RLock()
	defer c.RUnlock()
	out := make([]string, len(c.Subreddits))
	copy(out, c.Subreddits)
	return out
}

// AddSubreddit adds a subreddit if not already present.
func (c *Config) AddSubreddit(name string) bool {
	c.Lock()
	defer c.Unlock()
	for _, s := range c.Subreddits {
		if s == name {
			return false
		}
	}
	c.Subreddits = append(c.Subreddits, name)
	return true
}

// RemoveSubreddit removes a subreddit by name.
func (c *Config) RemoveSubreddit(name string) bool {
	c.Lock()
	defer c.Unlock()
	for i, s := range c.Subreddits {
		if s == name {
			c.Subreddits = append(c.Subreddits[:i], c.Subreddits[i+1:]...)
			return true
		}
	}
	return false
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
	if c.Subreddits == nil {
		c.Subreddits = []string{}
	}
}
