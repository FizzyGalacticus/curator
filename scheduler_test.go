package main

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCheckSource_StoresPostsAndUpdatesLastChecked(t *testing.T) {
	storage := newTestStorage(t)
	fetcher := &mockFetcher{
		fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
			return []Post{makePostSource("p1", SourceReddit, sub, 0)}, nil
		},
	}

	checkSource(context.Background(), fetcher, storage, testListID, SourceReddit, "pics", FetchCredentials{})

	posts := storage.GetPosts(testListID)
	if len(posts) != 1 || posts[0].ID != "p1" {
		t.Fatalf("want 1 post p1, got %v", posts)
	}
	if storage.GetLastChecked(testListID, SourceReddit, "pics").IsZero() {
		t.Error("want last-checked updated after a successful fetch")
	}
}

func TestCheckSource_FetchErrorDoesNotUpdateLastChecked(t *testing.T) {
	storage := newTestStorage(t)
	fetcher := &mockFetcher{
		fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
			return nil, fmt.Errorf("boom")
		},
	}

	checkSource(context.Background(), fetcher, storage, testListID, SourceReddit, "pics", FetchCredentials{})

	if !storage.GetLastChecked(testListID, SourceReddit, "pics").IsZero() {
		t.Error("want last-checked NOT updated after a fetch error")
	}
	if len(storage.GetPosts(testListID)) != 0 {
		t.Error("want no posts stored after a fetch error")
	}
}

func TestRunCheckPass_MergesAllThreeSourcesPerList(t *testing.T) {
	old := interCheckDelay
	interCheckDelay = time.Millisecond
	defer func() { interCheckDelay = old }()

	config := DefaultConfig()
	list, err := config.AddList(NewListInput{
		Name:             "Mixed",
		Subreddits:       []string{"pics"},
		FlickrGroups:     []string{"blackandwhite"},
		LemmyCommunities: []string{"pics@lemmy.world"},
	})
	if err != nil {
		t.Fatalf("AddList: %v", err)
	}

	storage := newTestStorage(t)

	redditFetcher := &mockFetcher{fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
		return []Post{makePostSource("r1", SourceReddit, sub, 0)}, nil
	}}
	flickrFetcher := &mockFetcher{fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
		return []Post{makePostSource("f1", SourceFlickr, sub, 0)}, nil
	}}
	lemmyFetcher := &mockFetcher{fn: func(sub string, since time.Time, creds FetchCredentials) ([]Post, error) {
		return []Post{makePostSource("l1", SourceLemmy, sub, 0)}, nil
	}}

	runCheckPass(context.Background(), config, storage, redditFetcher, flickrFetcher, lemmyFetcher)

	posts := storage.GetPosts(list.ID)
	if len(posts) != 3 {
		t.Fatalf("want 3 posts merged, got %d: %v", len(posts), posts)
	}
	ids := map[string]bool{}
	for _, p := range posts {
		ids[p.ID] = true
	}
	for _, want := range []string{"r1", "f1", "l1"} {
		if !ids[want] {
			t.Errorf("expected post %q in merged results", want)
		}
	}
}
