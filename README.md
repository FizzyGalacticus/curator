# Reddit Media Curator

A self-hosted Go application that monitors a list of subreddits for image, GIF, and video posts, displays them in a masonry grid UI, and lets you favorite and download media — no Reddit account or OAuth token required.

---

## How it works

The application has three concurrent components:

1. **Scheduler** — on startup and on a configurable interval, checks each configured subreddit for new posts, extracts media items, deduplicates, and writes new posts to storage. After each full pass, prunes old non-favorited posts.

2. **API server** — HTTP server on a configurable port (default `8080`) that serves both the REST API and the embedded single-page frontend.

3. **Frontend** — vanilla HTML/CSS/JS (no build step, no framework) embedded in the binary at compile time via `go:embed`.

---

## Post sources

Each subreddit check tries two sources, in order:

1. **Scrolller** (primary) — queries Scrolller's public GraphQL API (`api.scrolller.com/admin`) for the subreddit's most recent items. No authentication required. Scrolller doesn't expose post timestamps, so all returned items are accepted and rely on ID/URL-based deduplication in storage rather than a `since` cutoff.
2. **Reddit RSS** (fallback) — if Scrolller's request fails, falls back to Reddit's public RSS feed (`/r/{sub}/new.rss`). Only entries newer than the subreddit's last-checked time are kept.

### Scrolller media resolution

For each item, the client picks the highest-resolution video (`.mp4` preferred over `.webm`) if present, otherwise the highest-resolution image (`.jpg`/`.png` preferred over `.webp`), and uses the smallest available image as a thumbnail.

### Reddit media resolution

The Reddit RSS fetcher extracts the media URL from each entry's `<content>` HTML block. Reddit's content template always places links in a fixed order: `[permalink, /u/author, media-url, permalink]` — the third href is the actual media URL.

From that URL, the app applies the following resolution chain:

| URL pattern | Resolution |
|---|---|
| `i.redd.it/*.jpg/png/gif/webp` | Direct image — used as-is |
| `v.redd.it/{id}` | Constructs `https://v.redd.it/{id}/DASH_720.mp4` |
| `reddit.com/gallery/{id}` | Shows the RSS thumbnail image (gallery items can't be expanded without the JSON API) |
| `redgifs.com/watch/{slug}` or `/ifr/{slug}` | Calls the public RedGifs API (`api.redgifs.com/v2/gifs/{slug}`) |
| `imgur.com/a/{hash}` or `imgur.com/gallery/{hash}` | Calls the Imgur album API if `imgur_client_id` is configured |
| `i.imgur.com/...` or direct image URL | Used directly |
| `imgur.com/{id}` (single page) | Falls back to RSS thumbnail, or constructs `i.imgur.com/{id}.jpg` |
| `.mp4` / `.webm` direct link | Used as-is |
| Anything else (article links, YouTube, etc.) | Skipped — post not stored |

Reddit's public RSS feed requires no login, but the fetcher sends `over18` and `pref_quarantine_optin` cookies so age-gated or quarantined communities resolve the same way an authenticated request would.

---

## Data model

All state is stored in two JSON files in `$REDDIT_CURATOR_DATA_DIR` (default `/app/data`):

**`config.json`** — application settings, written on first run with defaults, editable at runtime via the Config tab or API:

```json
{
  "subreddits": ["pics", "wallpapers"],
  "check_interval": "30m",
  "download_dir": "/app/downloads",
  "api_port": 8080,
  "max_post_age_days": 30,
  "imgur_client_id": ""
}
```

**`data.json`** — posts, favorites, and per-subreddit last-checked timestamps:

```json
{
  "posts": [
    {
      "id": "abc123",
      "subreddit": "pics",
      "title": "...",
      "author": "username",
      "score": 0,
      "created_at": "2026-06-01T12:00:00Z",
      "permalink": "https://www.reddit.com/r/pics/comments/abc123/...",
      "media_items": [
        {
          "type": "image",
          "url": "https://i.redd.it/abc123.jpg",
          "thumbnail": "https://preview.redd.it/abc123.jpg?width=640...",
          "width": 1920,
          "height": 1080
        }
      ],
      "discovered_at": "2026-06-01T12:05:00Z"
    }
  ],
  "favorites": { "abc123": true },
  "last_checked": { "pics": "2026-06-01T12:05:00Z" }
}
```

`MediaType` values: `"image"`, `"video"`, `"gif"`.

> **Notes:**
> - `score` is always `0` — neither source exposes vote counts.
> - Posts sourced from Scrolller have `id` prefixed with `scrolller_` (e.g. `scrolller_123456`), an empty `author`, and a `created_at`/`discovered_at` set to fetch time (Scrolller doesn't expose original post timestamps).
> - Posts sourced from Reddit RSS keep Reddit's post ID, author, and original `created_at`.

---

## REST API

All responses have the shape `{ "success": bool, "data": ..., "message": "..." }`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/posts` | List posts. Query params: `filter=all\|favorites\|non-favorites`, `subreddit=name`. Sorted favorites-first then newest-first. |
| `POST` | `/api/posts/{id}/favorite` | Toggle favorite state. Returns `{ "favorited": bool }`. Triggers async download when newly favorited. |
| `GET` | `/api/config` | Read current config. |
| `PUT` | `/api/config` | Update config fields (`check_interval`, `download_dir`, `max_post_age_days`, `imgur_client_id`). Persisted immediately. |
| `GET` | `/api/subreddits` | List configured subreddits. |
| `POST` | `/api/subreddits` | Add a subreddit. Body: `{ "name": "pics" }`. Triggers immediate check. |
| `DELETE` | `/api/subreddits/{name}` | Remove a subreddit and prune its non-favorited posts. |
| `POST` | `/api/refresh` | Queue an immediate check of all subreddits. |
| `GET` | `/api/status` | Summary: post count, favorite count, subreddits, per-subreddit last-checked times. |

---

## Frontend

Single-page app served from embedded static files. Two tabs:

**Media tab**
- Masonry grid of post thumbnails
- Filter buttons: All / ★ Favorites / Hide Favorites
- Subreddit filter (via query param used internally)
- Manual refresh button
- Click any thumbnail to open the full-page viewer

**Full-page viewer**
- Shows full-size image, video (`<video autoplay loop>`), or GIF
- Multi-image posts: left/right navigation arrows + dot indicator
- Post navigation: up/down buttons to move between posts
- Favorite toggle (☆/★) — immediately persisted and triggers download
- External link to the original post's permalink (↗)
- Close with the ✕ button, Escape key, or clicking the backdrop

**Config tab**
- Add/remove subreddits
- Edit settings: check interval, download directory, max post age, Imgur Client ID
- Status panel: last-checked times per subreddit, manual refresh button

---

## Automatic download

When a post is favorited via the UI or API, `DownloadMedia` runs asynchronously and saves all media items to:

```
{download_dir}/{subreddit}/{post_id}/{index}.{ext}
```

Files are not re-downloaded if they already exist. Download errors are logged but do not affect the API response. Extension is inferred from `MediaType` (`video` → `.mp4`, `gif` → `.gif`) or from the URL.

---

## Scheduler behavior

- Runs immediately on startup, then on the `check_interval` ticker.
- For each subreddit, tries Scrolller first and falls back to Reddit RSS on error.
- Waits 2 seconds between subreddits to respect rate limits.
- A manual refresh (via API or UI) resets the ticker.
- After each full pass, prunes non-favorited posts older than `max_post_age_days` (0 = never prune).
- Reddit RSS results are filtered to posts newer than the subreddit's `last_checked` time. Scrolller results aren't (no timestamps available); deduplication by post ID and primary media URL prevents repeat storage either way.

---

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `REDDIT_CURATOR_DATA_DIR` | `/app/data` | Directory for `config.json` and `data.json` |
| `REDDIT_CURATOR_DOWNLOAD_DIR` | `/app/downloads` | Directory where favorited media is saved |
| `REDDIT_CURATOR_PORT` | `8080` | HTTP port — only applied on first run (when `api_port` is 0 in config) |

---

## Running

### Docker Compose (recommended)

```yaml
services:
  reddit-curator:
    image: reddit-curator:latest
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
      - ./downloads:/app/downloads
    environment:
      - TZ=America/Los_Angeles
      - REDDIT_CURATOR_DATA_DIR=/app/data
      - REDDIT_CURATOR_DOWNLOAD_DIR=/app/downloads
      - REDDIT_CURATOR_PORT=8080
    restart: unless-stopped
```

```sh
docker compose up --build
```

Open `http://localhost:8080` and add subreddits from the Config tab.

### Docker (standalone)

```sh
docker build -t reddit-curator .
docker run -p 8080:8080 \
  -v "$PWD/data:/app/data" \
  -v "$PWD/downloads:/app/downloads" \
  reddit-curator
```

### Local (Go 1.26+)

```sh
go run . 
```

Config and data files are written to `/app/data` by default. Override with `REDDIT_CURATOR_DATA_DIR`.

---

## Building

The Dockerfile uses a three-stage build:

1. **`golang:1.26-alpine`** — compiles a fully static binary (`CGO_ENABLED=0`, `-s -w` to strip debug info).
2. **`alpine:3.19`** — extracts the CA certificate bundle for HTTPS.
3. **`scratch`** — zero-OS final image containing only the binary and CA certs.

Final image size: ~7 MB.

The binary supports a `-health` flag used by the Docker `HEALTHCHECK` instruction:

```sh
/reddit-curator -health   # exits 0 if /api/status returns 200, else 1
```

---

## Code structure

```
main.go          — startup, env var handling, graceful shutdown
config.go        — Config struct, load/save/defaults, thread-safe accessors
storage.go       — Storage struct, Post/MediaItem types, data.json persistence
scheduler.go     — polling loop, PostFetcher interface, FallbackFetcher (Scrolller → Reddit)
scrolller.go     — Scrolller GraphQL client, media source selection
reddit.go        — RSS feed fetching, media URL extraction, RedGifs/Imgur API calls
downloader.go    — async file download triggered on favorite
api.go           — HTTP handlers, embedded static file server
static/
  index.html     — app shell, two-tab layout, full-page viewer markup
  app.js         — all frontend logic (no framework, no build step)
  style.css      — dark theme, masonry grid, viewer overlay
*_test.go        — unit/integration tests using httptest mock servers
```

### Key interfaces

**`PostFetcher`** (`scheduler.go`) — implemented by `*ScrolllerClient` and `*RedditClient`; allows scheduler tests to inject a mock without a live network connection. `FallbackFetcher` composes two `PostFetcher`s, trying the primary before falling back to the secondary.

**`httpDoer`** (`reddit.go`) — implemented by `*http.Client`; allows `RedditClient` and `ScrolllerClient` tests to inject a stub HTTP transport pointing at `httptest` servers.

---

## External dependencies

**None** — the module has no third-party Go dependencies (`go.mod` contains only `module` and `go` directives). All Scrolller, Reddit, RedGifs, and Imgur integration uses the standard library's `net/http`.

External services called at runtime:
- `api.scrolller.com` — Scrolller's public GraphQL API (`/admin`), primary post source
- `www.reddit.com` — RSS feed (`/r/{sub}/new.rss`), fallback post source
- `api.redgifs.com` — RedGifs public API (`/v2/gifs/{slug}`)
- `api.imgur.com` — Imgur album API (`/3/album/{hash}/images`, requires free Client ID)

---

## Tests

```sh
go test ./...
```

~80 tests covering config, storage, Scrolller and RSS/media parsing paths, the scheduler fallback behavior, all API handlers, and the downloader. Uses `httptest.NewServer` for Scrolller, Reddit, RedGifs, and Imgur mocks — no live network calls.
