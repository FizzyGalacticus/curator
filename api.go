package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

// APIServer holds dependencies for all HTTP handlers.
type APIServer struct {
	config    *Config
	storage   *Storage
	configPath string
	refreshCh chan<- struct{}
}

// PostWithFavorite is the API representation of a Post, including the caller's
// favorite state as a convenience field so the frontend doesn't need to join.
type PostWithFavorite struct {
	Post
	Favorited bool `json:"favorited"`
}

// StartAPIServer starts the HTTP server and blocks until ctx is cancelled.
func StartAPIServer(ctx context.Context, config *Config, storage *Storage, configPath string, refreshCh chan<- struct{}) {
	api := &APIServer{
		config:     config,
		storage:    storage,
		configPath: configPath,
		refreshCh:  refreshCh,
	}

	mux := http.NewServeMux()

	// API routes
	mux.HandleFunc("/api/posts", api.handlePosts)
	mux.HandleFunc("/api/posts/", api.handlePostByID)
	mux.HandleFunc("/api/config", api.handleConfig)
	mux.HandleFunc("/api/subreddits", api.handleSubreddits)
	mux.HandleFunc("/api/subreddits/", api.handleSubredditByName)
	mux.HandleFunc("/api/refresh", api.handleRefresh)
	mux.HandleFunc("/api/status", api.handleStatus)

	// Static file server (embedded)
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to load embedded static files: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(staticFS)))

	config.RLock()
	addr := fmt.Sprintf(":%d", config.APIPort)
	config.RUnlock()

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		log.Printf("API server listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("API server error: %v", err)
		}
	}()

	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("API server shutdown error: %v", err)
	}
	log.Println("API server stopped")
}

// ---- Posts ----

func (api *APIServer) handlePosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	posts := api.storage.GetPosts()
	favs := api.storage.GetFavorites()

	filter := r.URL.Query().Get("filter") // "all" | "favorites" | "non-favorites"
	subredditFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("subreddit")))

	var result []PostWithFavorite
	for _, p := range posts {
		isFav := favs[p.ID]
		if filter == "favorites" && !isFav {
			continue
		}
		if filter == "non-favorites" && isFav {
			continue
		}
		if subredditFilter != "" && strings.ToLower(p.Subreddit) != subredditFilter {
			continue
		}
		result = append(result, PostWithFavorite{Post: p, Favorited: isFav})
	}

	// Sort: favorites first, then newest first.
	sort.Slice(result, func(i, j int) bool {
		if result[i].Favorited != result[j].Favorited {
			return result[i].Favorited
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	apiSuccess(w, result)
}

func (api *APIServer) handlePostByID(w http.ResponseWriter, r *http.Request) {
	// Expect /api/posts/{id}/favorite
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[3] != "favorite" {
		apiError(w, http.StatusNotFound, "not found")
		return
	}
	postID := parts[2]

	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	nowFav, err := api.storage.ToggleFavorite(postID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to toggle favorite: %v", err))
		return
	}

	// When newly favorited, download media asynchronously.
	if nowFav {
		posts := api.storage.GetPosts()
		for _, p := range posts {
			if p.ID == postID {
				api.config.RLock()
				dlDir := api.config.DownloadDir
				api.config.RUnlock()
				go DownloadMedia(p, dlDir)
				break
			}
		}
	}

	apiSuccess(w, map[string]bool{"favorited": nowFav})
}

// ---- Config ----

func (api *APIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		api.config.RLock()
		defer api.config.RUnlock()
		apiSuccess(w, map[string]interface{}{
			"check_interval":    api.config.CheckInterval,
			"download_dir":      api.config.DownloadDir,
			"api_port":          api.config.APIPort,
			"max_post_age_days": api.config.MaxPostAgeDays,
			"subreddits":        api.config.Subreddits,
			"imgur_client_id":   api.config.ImgurClientID,
		})

	case http.MethodPut:
		var updates map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
			apiError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		api.config.Lock()
		if v, ok := updates["check_interval"].(string); ok && v != "" {
			api.config.CheckInterval = v
		}
		if v, ok := updates["download_dir"].(string); ok {
			api.config.DownloadDir = v
		}
		if v, ok := updates["max_post_age_days"].(float64); ok {
			api.config.MaxPostAgeDays = int(v)
		}
		if v, ok := updates["imgur_client_id"].(string); ok {
			api.config.ImgurClientID = v
		}
		api.config.Unlock()

		if err := api.config.Save(api.configPath); err != nil {
			apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
			return
		}

		log.Println("Config updated via API")
		apiSuccess(w, map[string]string{"message": "config updated"})

	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---- Subreddits ----

func (api *APIServer) handleSubreddits(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		apiSuccess(w, api.config.GetSubreddits())

	case http.MethodPost:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			apiError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		name := strings.ToLower(strings.TrimSpace(req.Name))
		if name == "" {
			apiError(w, http.StatusBadRequest, "name is required")
			return
		}

		added := api.config.AddSubreddit(name)
		if err := api.config.Save(api.configPath); err != nil {
			apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
			return
		}

		// Trigger an immediate check for the new subreddit.
		if added {
			select {
			case api.refreshCh <- struct{}{}:
			default:
			}
		}

		log.Printf("Subreddit added: r/%s", name)
		apiSuccess(w, map[string]interface{}{"name": name, "added": added})

	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (api *APIServer) handleSubredditByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 {
		apiError(w, http.StatusBadRequest, "subreddit name required")
		return
	}
	name := strings.ToLower(parts[2])

	api.config.RemoveSubreddit(name)
	if err := api.config.Save(api.configPath); err != nil {
		apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
		return
	}

	// Remove non-favorited posts for this subreddit.
	if err := api.storage.RemoveSubredditData(name); err != nil {
		log.Printf("Warning: failed to remove data for r/%s: %v", name, err)
	}

	log.Printf("Subreddit removed: r/%s", name)
	apiSuccess(w, map[string]string{"name": name})
}

// ---- Refresh ----

func (api *APIServer) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	select {
	case api.refreshCh <- struct{}{}:
		log.Println("Manual refresh queued")
	default:
		// Channel full; a refresh is already pending.
	}

	apiSuccess(w, map[string]string{"message": "refresh queued"})
}

// ---- Status ----

func (api *APIServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	posts := api.storage.GetPosts()
	favs := api.storage.GetFavorites()
	subs := api.config.GetSubreddits()

	lastChecked := make(map[string]string, len(subs))
	for _, s := range subs {
		t := api.storage.GetLastChecked(s)
		if t.IsZero() {
			lastChecked[s] = "never"
		} else {
			lastChecked[s] = t.Format(time.RFC3339)
		}
	}

	apiSuccess(w, map[string]interface{}{
		"posts_count":     len(posts),
		"favorites_count": len(favs),
		"subreddits":      subs,
		"last_checked":    lastChecked,
	})
}

// ---- Helpers ----

type apiResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

func apiSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiResponse{Success: true, Data: data})
}

func apiError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(apiResponse{Success: false, Message: msg})
}
