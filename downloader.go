package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DownloadMedia saves all media items for a post to disk under downloadDir.
// Files are saved as: {downloadDir}/{subreddit}/{postID}_{index}.{ext}
// Errors are logged but not returned—download failures must not block the API response.
func DownloadMedia(post Post, downloadDir string) {
	if downloadDir == "" {
		return
	}

	dir := filepath.Join(downloadDir, sanitizeDirName(post.Subreddit), post.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("Download: failed to create dir %s: %v", dir, err)
		return
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	for i, item := range post.MediaItems {
		ext := extFromMediaItem(item)
		dest := filepath.Join(dir, fmt.Sprintf("%d%s", i, ext))

		if _, err := os.Stat(dest); err == nil {
			continue // already downloaded
		}

		if err := downloadFile(client, item.URL, dest); err != nil {
			log.Printf("Download: failed to download %s: %v", item.URL, err)
		} else {
			log.Printf("Download: saved %s", dest)
		}
	}
}

func downloadFile(client *http.Client, url, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", redditUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func extFromMediaItem(item MediaItem) string {
	switch item.Type {
	case MediaVideo:
		return ".mp4"
	case MediaGif:
		return ".gif"
	default:
		// Try to derive from URL
		url := strings.Split(item.URL, "?")[0]
		url = strings.ToLower(url)
		for _, ext := range []string{".jpg", ".jpeg", ".png", ".webp", ".gif"} {
			if strings.HasSuffix(url, ext) {
				return ext
			}
		}
		return ".jpg"
	}
}

func sanitizeDirName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	return b.String()
}
