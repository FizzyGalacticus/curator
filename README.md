# Curator

A self-hosted Go application that aggregates image, GIF, and video posts from Reddit, Flickr, and Lemmy, displays them in a masonry grid UI, and lets you favorite and download media — no account or OAuth token required for Reddit or Lemmy (Flickr needs a free API key).

You can create multiple independent **curation lists** (e.g. a "SFW" list watching r/pics + r/wallpapers, and a separate list watching a different set of subreddits, Flickr groups, and Lemmy communities). Each list has its own sources, posts, and favorites — nothing is shared between lists, even if they happen to watch the same subreddit, Flickr group, or Lemmy community.

Every source in a list is fetched and merged into the same pool — they're additive, not fallback alternatives to each other — and cross-source duplicates (the same image posted to more than one source) are deduplicated automatically.

---

## How it works

The application has three concurrent components:

1. **Scheduler** — on startup and on a configurable interval, checks every subreddit, Flickr group, and Lemmy community in every curation list for new posts, extracts media items, deduplicates, and writes new posts to storage. After each full pass, prunes old non-favorited posts in every list.

2. **API server** — HTTP server on a configurable port (default `8080`) that serves both the REST API and the embedded single-page frontend.

3. **Frontend** — vanilla HTML/CSS/JS (no build step, no framework) embedded in the binary at compile time via `go:embed`.

---

## Post sources

A curation list can combine three independent source categories. All three are checked and their results **merged** into the same list — they are not fallback alternatives to each other:

- **Reddit** (`subreddits`) — each subreddit is checked against Scrolller first, falling back to Reddit's own RSS feed on error (these two mirror the same subreddit content, so a fallback pairing makes sense here).
- **Flickr** (`flickr_groups`) — each entry is a Flickr **group**'s URL slug (curated photo-submission pools, closer to a subreddit than Flickr's broader tag search). Requires a free `flickr_api_key`.
- **Lemmy** (`lemmy_communities`) — each entry is a self-contained `"community@instance"` string (e.g. `"pics@lemmy.world"`), since Lemmy is federated and the same community name can exist on many different instances. No credential required.

### Reddit: Scrolller (primary) → Reddit RSS (fallback)

1. **Scrolller** (primary) — queries Scrolller's public GraphQL API (`api.scrolller.com/admin`) for the subreddit's most recent items. No authentication required. Scrolller doesn't expose post timestamps, so all returned items are accepted (no `since` cutoff) and each is given a synthetic `created_at` spread linearly across the time since that subreddit was last checked — assuming Scrolller's default feed order is newest-first — rather than stamping every item in the batch with the same fetch instant. Otherwise a whole subreddit's ~50 items would sort as one solid block in the UI instead of interleaving with posts from other subreddits/sources by real recency.
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

### Flickr media resolution

A group's URL slug (e.g. `blackandwhite` from `flickr.com/groups/blackandwhite/pool/`) is resolved to Flickr's numeric group NSID via `flickr.urls.lookupGroup`, then cached in memory — the resolution only happens once per group, not on every scheduler pass. Photos are fetched via `flickr.groups.pools.getPhotos`, preferring the original size (`url_o`) over large (`url_l`) over medium (`url_m`), with the square thumbnail (`url_sq`) used in the grid. This endpoint has no server-side date filter, so every photo's upload time is checked against the group's last-checked time rather than assuming the results are strictly ordered.

**Photos only** — Flickr's public API (API-key-only, no OAuth) can't resolve playable video file URLs; that requires an OAuth 1.0a-signed `flickr.videos.getStreams` call needing a full user account and app secret, which is out of scope for a simple API key. Video items are skipped.

### Lemmy media resolution

Each `"community@instance"` identifier is split into its community name and instance host, then queried against that instance's public `GET /api/v3/post/list?community_name={name}&sort=New` endpoint — no authentication needed. A post's `url` field is classified as image/gif/video using the same URL-suffix heuristics as the Reddit RSS fetcher. Both `post.nsfw` and `community.nsfw` are checked and skipped as a defensive safety net. Featured/pinned posts can appear out of chronological order even with `sort=New`, so every post's `published` time is checked against the community's last-checked time rather than stopping at the first post that looks old enough.

---

## Data model

All state is stored in two JSON files in `$CURATOR_DATA_DIR` (default `/app/data`):

**`config.json`** — curation lists and global application settings, written on first run with defaults, editable at runtime via the UI or API:

```json
{
  "lists": [
    {
      "id": "a1b2c3d4",
      "name": "SFW",
      "subreddits": ["pics", "wallpapers"],
      "flickr_groups": ["blackandwhite"],
      "lemmy_communities": ["pics@lemmy.world"]
    }
  ],
  "check_interval": "30m",
  "download_dir": "/app/downloads",
  "api_port": 8080,
  "max_post_age_days": 30,
  "imgur_client_id": "",
  "flickr_api_key": ""
}
```

`check_interval`, `download_dir`, `api_port`, `max_post_age_days`, `imgur_client_id`, and `flickr_api_key` are global — they apply the same way to every curation list.

**`data.json`** — posts, favorites, and per-source last-checked timestamps, keyed by curation list ID. Each list's data is fully independent: if the same subreddit, Flickr group, or Lemmy community appears in two lists (or under two different source types in the same list), it's fetched, stored, and deduplicated separately for each.

```json
{
  "lists": {
    "a1b2c3d4": {
      "posts": [
        {
          "id": "abc123",
          "source": "reddit",
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
      "last_checked": {
        "reddit:pics": "2026-06-01T12:05:00Z",
        "flickr:blackandwhite": "2026-06-01T12:05:30Z",
        "lemmy:pics@lemmy.world": "2026-06-01T12:06:00Z"
      }
    }
  }
}
```

`source` values: `"reddit"` (covers both Scrolller- and Reddit-RSS-sourced posts — they mirror the same subreddit content), `"flickr"`, `"lemmy"`. `MediaType` values: `"image"`, `"video"`, `"gif"`. `last_checked` keys are namespaced `"{source}:{identifier}"` so the same literal name under two different sources never collides.

> **Notes:**
> - `score` is always `0` — none of the three sources expose vote counts.
> - Posts sourced from Scrolller have `id` prefixed with `scrolller_` (e.g. `scrolller_123456`), an empty `author`, a `discovered_at` set to fetch time, and a synthetic `created_at` spread across the since-last-checked window (see "Scrolller media resolution" above) rather than the true original post time, which Scrolller doesn't expose.
> - Posts sourced from Reddit RSS keep Reddit's post ID, author, and original `created_at`.
> - Posts sourced from Flickr have `id` prefixed with `flickr_`; Lemmy posts are prefixed `lemmy_{instance}_` to stay globally unique across instances.
> - This schema has no migration path from earlier versions of this app — if upgrading, re-create your subreddits/groups/communities via the UI.

---

## REST API

All responses have the shape `{ "success": bool, "data": ..., "message": "..." }`.

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/lists` | List curation lists. Each entry: `{ id, name, subreddits, flickr_groups, lemmy_communities, post_count, favorite_count }`. |
| `POST` | `/api/lists` | Create a list. Body: `{ "name": "SFW", "subreddits": [...], "flickr_groups": [...], "lemmy_communities": [...] }` (all source fields optional). Triggers an immediate check if any were given. |
| `GET` | `/api/lists/{id}` | Read one list's detail: `{ id, name, subreddits, flickr_groups, lemmy_communities }`. |
| `PUT` | `/api/lists/{id}` | Rename a list. Body: `{ "name": "..." }`. |
| `DELETE` | `/api/lists/{id}` | Delete a list and **all** of its data — posts and favorites included. |
| `GET` | `/api/lists/{id}/posts` | List posts in this list. Query params: `filter=all\|favorites\|non-favorites`, `subreddit=name`. Sorted favorites-first then newest-first. |
| `POST` | `/api/lists/{id}/posts/{postId}/favorite` | Toggle favorite state. Returns `{ "favorited": bool }`. Triggers async download when newly favorited. |
| `POST` | `/api/lists/{id}/subreddits` | Add a subreddit to this list. Body: `{ "name": "pics" }`. Triggers immediate check. |
| `DELETE` | `/api/lists/{id}/subreddits/{name}` | Remove a subreddit from this list and prune its non-favorited posts. |
| `POST` | `/api/lists/{id}/flickr-groups` | Add a Flickr group to this list. Body: `{ "name": "blackandwhite" }` (a full group URL is also accepted and normalized). Triggers immediate check. |
| `DELETE` | `/api/lists/{id}/flickr-groups/{name}` | Remove a Flickr group from this list and prune its non-favorited posts. |
| `POST` | `/api/lists/{id}/lemmy-communities` | Add a Lemmy community to this list. Body: `{ "name": "pics@lemmy.world" }`. Triggers immediate check. |
| `DELETE` | `/api/lists/{id}/lemmy-communities/{name}` | Remove a Lemmy community from this list and prune its non-favorited posts. |
| `POST` | `/api/lists/{id}/refresh` | Queue an immediate check of **all** lists (the scheduler's refresh trigger is global, not per-list). |
| `GET` | `/api/lists/{id}/status` | Summary for this list: post count, favorite count, subreddits/flickr_groups/lemmy_communities, and `last_checked` nested per source: `{ "reddit": {...}, "flickr": {...}, "lemmy": {...} }`. |
| `GET` | `/api/config` | Read global settings (no longer includes subreddits). |
| `PUT` | `/api/config` | Update global settings (`check_interval`, `download_dir`, `max_post_age_days`, `imgur_client_id`, `flickr_api_key`). Persisted immediately. |

Unknown list IDs return `404` on every `/api/lists/{id}...` route.

---

## Frontend

Single-page app served from embedded static files, with a small hash-based router (`#/`, `#/list/{id}`, `#/settings`) across three views:

**Home view** (`#/`)
- Card grid of saved curation lists: name, subreddit count, post count, favorite count
- Create a new list (name + optional comma/newline-separated subreddits)
- Rename or delete a list from its card (delete removes **all** of that list's data, including favorites)
- Gear icon opens the global Settings view

**List view** (`#/list/{id}`) — same Media/Config tabs as before, scoped to the open list:

*Media tab*
- Masonry grid of post thumbnails, each labeled with its source (`r/name` for Reddit, `flickr/name` for a Flickr group, `!name` for a Lemmy community — Lemmy's own community-reference convention)
- Filter buttons: All / ★ Favorites / Hide Favorites
- Subreddit filter (via query param used internally)
- Manual refresh button
- Click any thumbnail to open the full-page viewer

*Config tab*
- Three identical add/remove sections — Subreddits, Flickr Groups, Lemmy Communities — driven by one shared editor component in `app.js` differing only in API endpoint, display prefix, and input normalization
- Status panel: last-checked times per source, manual refresh button

**Settings view** (`#/settings`, global — not list-scoped)
- Check interval, download directory, max post age, Imgur Client ID, Flickr API Key

**Full-page viewer** (available from any list's Media tab)
- Shows full-size image, video (`<video autoplay loop>`), or GIF
- Multi-image posts: left/right navigation arrows + dot indicator
- Post navigation: up/down buttons to move between posts
- Favorite toggle (☆/★) — immediately persisted and triggers download
- External link to the original post's permalink (↗)
- Close with the ✕ button, Escape key, or clicking the backdrop

---

## Automatic download

When a post is favorited via the UI or API, `DownloadMedia` runs asynchronously and saves all media items to:

```
{download_dir}/{subreddit}/{post_id}/{index}.{ext}
```

For non-Reddit posts, `{subreddit}` is a Flickr group slug or a Lemmy `community@instance` string rather than a subreddit name — it's passed through the same filesystem-sanitizing helper regardless of source. Files are not re-downloaded if they already exist. Download errors are logged but do not affect the API response. Extension is inferred from `MediaType` (`video` → `.mp4`, `gif` → `.gif`) or from the URL.

---

## Scheduler behavior

- Runs immediately on startup, then on the `check_interval` ticker.
- Checks every subreddit, Flickr group, and Lemmy community in every curation list. Each (list, source, identifier) triple is fetched, stored, and deduplicated **completely independently** — if the same identifier appears in two lists, or the same literal name appears under two different sources in one list, it's checked separately and each keeps its own copy of the resulting posts and last-checked time.
- All three source categories are checked and their results **merged** into the same list's post pool — Flickr and Lemmy are not fallback alternatives to Reddit, they're additive. Only Reddit itself has an internal fallback pairing: Scrolller first, Reddit RSS on error.
- Waits 2 seconds between every individual check (across all lists and all sources combined) to respect rate limits.
- A manual refresh (via API or UI) resets the ticker and re-checks **every** list and source, not just the one it was triggered from.
- After each full pass, prunes non-favorited posts older than `max_post_age_days` in every list (0 = never prune; this setting is global, not per-list, not per-source).
- Reddit RSS, Flickr, and Lemmy results are filtered to posts newer than the identifier's `last_checked` time (per list). Scrolller has no real timestamps to filter by, so all returned items are accepted and given a synthetic `created_at` spread across the since-last-checked window instead (see "Scrolller media resolution" above) — this keeps the UI's date-sorted view interleaving properly across subreddits/sources rather than showing one block per subreddit. Deduplication by post ID and primary media URL prevents repeat storage either way, scoped to each list.

---

## Environment variables

| Variable | Default | Description |
|---|---|---|
| `CURATOR_DATA_DIR` | `/app/data` | Directory for `config.json` and `data.json` |
| `CURATOR_DOWNLOAD_DIR` | `/app/downloads` | Directory where favorited media is saved |
| `CURATOR_PORT` | `8080` | HTTP port — only applied on first run (when `api_port` is 0 in config) |

---

## Running

### Docker Compose (recommended)

```yaml
services:
  curator:
    image: curator:latest
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
      - ./downloads:/app/downloads
    environment:
      - TZ=America/Los_Angeles
      - CURATOR_DATA_DIR=/app/data
      - CURATOR_DOWNLOAD_DIR=/app/downloads
      - CURATOR_PORT=8080
    restart: unless-stopped
```

```sh
docker compose up --build
```

Open `http://localhost:8080`, create a curation list, and add subreddits to it from the list's Config tab.

### Docker (standalone)

```sh
docker build -t curator .
docker run -p 8080:8080 \
  -v "$PWD/data:/app/data" \
  -v "$PWD/downloads:/app/downloads" \
  curator
```

### Local (Go 1.26+)

```sh
go run . 
```

Config and data files are written to `/app/data` by default. Override with `CURATOR_DATA_DIR`.

---

## Building

The Dockerfile uses a three-stage build:

1. **`golang:1.26-alpine`** — compiles a fully static binary (`CGO_ENABLED=0`, `-s -w` to strip debug info).
2. **`alpine:3.19`** — extracts the CA certificate bundle for HTTPS.
3. **`scratch`** — zero-OS final image containing only the binary and CA certs.

Final image size: ~7 MB.

The binary supports a `-health` flag used by the Docker `HEALTHCHECK` instruction:

```sh
/curator -health   # exits 0 if /api/status returns 200, else 1
```

---

## Code structure

```
main.go          — startup, env var handling, graceful shutdown
config.go        — Config struct, load/save/defaults, thread-safe accessors
storage.go       — Storage struct, Post/MediaItem/PostSource types, data.json persistence
scheduler.go     — polling loop, PostFetcher interface, FetchCredentials, FallbackFetcher (Scrolller → Reddit)
scrolller.go     — Scrolller GraphQL client, media source selection
reddit.go        — RSS feed fetching, media URL extraction, RedGifs/Imgur API calls
flickr.go        — Flickr group-pool client, group NSID resolution + cache
lemmy.go         — Lemmy federated post-list client, community@instance parsing
downloader.go    — async file download triggered on favorite
api.go           — HTTP handlers, embedded static file server
static/
  index.html     — app shell: home/list/settings views, full-page viewer markup
  app.js         — all frontend logic (no framework, no build step)
  style.css      — dark theme, masonry grid, viewer overlay
*_test.go        — unit/integration tests using httptest mock servers
```

### Key interfaces

**`PostFetcher`** (`scheduler.go`) — implemented by `*ScrolllerClient`, `*RedditClient`, `*FlickrClient`, and `*LemmyClient`; allows scheduler tests to inject a mock without a live network connection. Takes a `FetchCredentials` struct (bundles `ImgurClientID` and `FlickrAPIKey`) so each fetcher can ignore the credentials it doesn't need. `FallbackFetcher` composes two `PostFetcher`s, trying the primary before falling back to the secondary — used only for the Reddit pairing; Flickr and Lemmy are queried independently and merged, not composed via `FallbackFetcher`.

**`httpDoer`** (`reddit.go`) — implemented by `*http.Client`; allows `RedditClient`, `ScrolllerClient`, `FlickrClient`, and `LemmyClient` tests to inject a stub HTTP transport pointing at `httptest` servers.

---

## External dependencies

**None** — the module has no third-party Go dependencies (`go.mod` contains only `module` and `go` directives). All Scrolller, Reddit, RedGifs, Imgur, Flickr, and Lemmy integration uses the standard library's `net/http`.

External services called at runtime:
- `api.scrolller.com` — Scrolller's public GraphQL API (`/admin`), primary Reddit post source
- `www.reddit.com` — RSS feed (`/r/{sub}/new.rss`), fallback Reddit post source
- `api.redgifs.com` — RedGifs public API (`/v2/gifs/{slug}`)
- `api.imgur.com` — Imgur album API (`/3/album/{hash}/images`, requires free Client ID)
- `api.flickr.com` — Flickr REST API (`flickr.urls.lookupGroup`, `flickr.groups.pools.getPhotos`), requires a free API key
- arbitrary Lemmy instance hosts (from each configured `community@instance` identifier) — public `GET /api/v3/post/list`, no auth

---

## Tests

```sh
go test ./...
```

~130 tests covering config (including multi-source list CRUD and identifier normalization), storage (including per-list and per-source isolation), Scrolller and RSS/media parsing paths, Flickr group resolution/caching, Lemmy identifier parsing and post filtering, the scheduler's multi-source aggregation and fallback behavior, all API handlers (including list/source CRUD and 404s on unknown list IDs), and the downloader. Uses `httptest.NewServer` for Scrolller, Reddit, RedGifs, Imgur, Flickr, and Lemmy mocks — no live network calls (except a one-time manual `curl` verification against a real Lemmy instance during development).
