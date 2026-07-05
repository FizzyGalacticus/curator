package main

import (
	"context"
	"log"
	"time"
)

// interCheckDelay is the pause between individual source checks (across every
// list and every source), to respect upstream rate limits. Overridable in tests.
var interCheckDelay = 2 * time.Second

// FetchCredentials bundles the optional per-source credentials a PostFetcher
// may need. Fetchers that don't need a credential simply ignore it.
type FetchCredentials struct {
	ImgurClientID string
	FlickrAPIKey  string
}

// PostFetcher abstracts new-post retrieval so the scheduler can be tested
// without a live network connection.
type PostFetcher interface {
	FetchNewPosts(query string, since time.Time, creds FetchCredentials) ([]Post, error)
}

// FallbackFetcher tries Primary first; on error it falls back to Secondary.
type FallbackFetcher struct {
	Primary   PostFetcher
	Secondary PostFetcher
}

func (f *FallbackFetcher) FetchNewPosts(query string, since time.Time, creds FetchCredentials) ([]Post, error) {
	posts, err := f.Primary.FetchNewPosts(query, since, creds)
	if err != nil {
		log.Printf("Scheduler: primary source failed for r/%s (%v), trying fallback", query, err)
		return f.Secondary.FetchNewPosts(query, since, creds)
	}
	return posts, nil
}

// RunScheduler creates real clients and delegates to RunSchedulerWithFetchers.
// Reddit keeps its Scrolller->RSS fallback pairing (they mirror the same
// subreddit content); Flickr and Lemmy are separate, additive sources merged
// into the same list's post pool alongside Reddit, not fallback alternatives.
func RunScheduler(ctx context.Context, config *Config, storage *Storage, refreshCh <-chan struct{}) {
	redditFetcher := &FallbackFetcher{
		Primary:   NewScrolllerClient(),
		Secondary: NewRedditClient(),
	}
	RunSchedulerWithFetchers(ctx, config, storage, refreshCh, redditFetcher, NewFlickrClient(), NewLemmyClient())
}

// RunSchedulerWithFetchers is the testable core: it accepts any PostFetcher
// per source instead of concrete clients.
func RunSchedulerWithFetchers(ctx context.Context, config *Config, storage *Storage, refreshCh <-chan struct{}, redditFetcher, flickrFetcher, lemmyFetcher PostFetcher) {
	log.Println("Scheduler started")

	checkAll := func() {
		runCheckPass(ctx, config, storage, redditFetcher, flickrFetcher, lemmyFetcher)
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

// runCheckPass checks every source identifier in every list once, then prunes
// old posts in every list. Reddit, Flickr, and Lemmy identifiers are all
// checked and merged into the same list's post pool.
func runCheckPass(ctx context.Context, config *Config, storage *Storage, redditFetcher, flickrFetcher, lemmyFetcher PostFetcher) {
	lists := config.GetLists()

	config.RLock()
	creds := FetchCredentials{ImgurClientID: config.ImgurClientID, FlickrAPIKey: config.FlickrAPIKey}
	config.RUnlock()

	for _, list := range lists {
		for _, sub := range list.Subreddits {
			if !checkSourceRateLimited(ctx, redditFetcher, storage, list.ID, SourceReddit, sub, creds) {
				return
			}
		}
		for _, group := range list.FlickrGroups {
			if !checkSourceRateLimited(ctx, flickrFetcher, storage, list.ID, SourceFlickr, group, creds) {
				return
			}
		}
		for _, community := range list.LemmyCommunities {
			if !checkSourceRateLimited(ctx, lemmyFetcher, storage, list.ID, SourceLemmy, community, creds) {
				return
			}
		}
	}

	config.RLock()
	maxAge := config.MaxPostAgeDays
	config.RUnlock()
	for _, list := range lists {
		if err := storage.PruneOldPosts(list.ID, maxAge); err != nil {
			log.Printf("Scheduler: prune error for list %q: %v", list.Name, err)
		}
	}
}

// checkSourceRateLimited runs checkSource, then waits interCheckDelay before
// returning true, or returns false without waiting if ctx is done.
func checkSourceRateLimited(ctx context.Context, fetcher PostFetcher, storage *Storage, listID string, source PostSource, name string, creds FetchCredentials) bool {
	if ctx.Err() != nil {
		return false
	}
	checkSource(ctx, fetcher, storage, listID, source, name, creds)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(interCheckDelay):
		return true
	}
}

func checkSource(ctx context.Context, fetcher PostFetcher, storage *Storage, listID string, source PostSource, name string, creds FetchCredentials) {
	if ctx.Err() != nil {
		return
	}

	since := storage.GetLastChecked(listID, source, name)
	log.Printf("Scheduler: checking %s (list %s, since %s)", lastCheckedKey(source, name), listID, since.Format(time.RFC3339))

	posts, err := fetcher.FetchNewPosts(name, since, creds)
	if err != nil {
		log.Printf("Scheduler: error fetching %s: %v", lastCheckedKey(source, name), err)
		return
	}

	if len(posts) > 0 {
		if err := storage.AddPosts(listID, posts); err != nil {
			log.Printf("Scheduler: error storing posts for %s: %v", lastCheckedKey(source, name), err)
			return
		}
		log.Printf("Scheduler: added %d posts from %s (list %s)", len(posts), lastCheckedKey(source, name), listID)
	}

	if err := storage.SetLastChecked(listID, source, name, time.Now().UTC()); err != nil {
		log.Printf("Scheduler: error updating last-checked for %s: %v", lastCheckedKey(source, name), err)
	}
}
