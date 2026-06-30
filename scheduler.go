package main

import (
	"context"
	"log"
	"time"
)

// PostFetcher abstracts new-post retrieval so the scheduler can be tested
// without a live Reddit connection.
type PostFetcher interface {
	FetchNewPosts(subreddit string, since time.Time, imgurClientID string) ([]Post, error)
}

// FallbackFetcher tries Primary first; on error it falls back to Secondary.
type FallbackFetcher struct {
	Primary   PostFetcher
	Secondary PostFetcher
}

func (f *FallbackFetcher) FetchNewPosts(subreddit string, since time.Time, imgurClientID string) ([]Post, error) {
	posts, err := f.Primary.FetchNewPosts(subreddit, since, imgurClientID)
	if err != nil {
		log.Printf("Scheduler: primary source failed for r/%s (%v), trying fallback", subreddit, err)
		return f.Secondary.FetchNewPosts(subreddit, since, imgurClientID)
	}
	return posts, nil
}

// RunScheduler creates real clients and delegates to RunSchedulerWithFetcher.
func RunScheduler(ctx context.Context, config *Config, storage *Storage, refreshCh <-chan struct{}) {
	fetcher := &FallbackFetcher{
		Primary:   NewScrolllerClient(),
		Secondary: NewRedditClient(),
	}
	RunSchedulerWithFetcher(ctx, config, storage, refreshCh, fetcher)
}

// RunSchedulerWithFetcher is the testable core: it accepts any PostFetcher
// instead of a concrete *RedditClient.
func RunSchedulerWithFetcher(ctx context.Context, config *Config, storage *Storage, refreshCh <-chan struct{}, fetcher PostFetcher) {
	log.Println("Scheduler started")

	checkAll := func() {
		subs := config.GetSubreddits()
		for _, sub := range subs {
			select {
			case <-ctx.Done():
				return
			default:
			}
			checkSubreddit(ctx, fetcher, config, storage, sub)
			// Respect Reddit's rate limit between subreddits.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		config.RLock()
		maxAge := config.MaxPostAgeDays
		config.RUnlock()
		if err := storage.PruneOldPosts(maxAge); err != nil {
			log.Printf("Scheduler: prune error: %v", err)
		}
	}

	// Run an initial pass immediately on startup.
	checkAll()

	interval := config.GetCheckInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("Scheduler stopped")
			return
		case <-refreshCh:
			log.Println("Scheduler: manual refresh triggered")
			ticker.Reset(config.GetCheckInterval())
			checkAll()
		case <-ticker.C:
			newInterval := config.GetCheckInterval()
			if newInterval != interval {
				ticker.Reset(newInterval)
				interval = newInterval
			}
			checkAll()
		}
	}
}

func checkSubreddit(ctx context.Context, fetcher PostFetcher, config *Config, storage *Storage, sub string) {
	if ctx.Err() != nil {
		return
	}

	since := storage.GetLastChecked(sub)
	log.Printf("Scheduler: checking r/%s (since %s)", sub, since.Format(time.RFC3339))

	config.RLock()
	imgurClientID := config.ImgurClientID
	config.RUnlock()

	posts, err := fetcher.FetchNewPosts(sub, since, imgurClientID)
	if err != nil {
		log.Printf("Scheduler: error fetching r/%s: %v", sub, err)
		return
	}

	if len(posts) > 0 {
		if err := storage.AddPosts(posts); err != nil {
			log.Printf("Scheduler: error storing posts for r/%s: %v", sub, err)
			return
		}
		log.Printf("Scheduler: added %d posts from r/%s", len(posts), sub)
	}

	if err := storage.SetLastChecked(sub, time.Now().UTC()); err != nil {
		log.Printf("Scheduler: error updating last-checked for r/%s: %v", sub, err)
	}
}
