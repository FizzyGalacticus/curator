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
	config     *Config
	storage    *Storage
	configPath string
	refreshCh  chan<- struct{}
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

	// Lists
	mux.HandleFunc("GET /api/lists", api.handleListsIndex)
	mux.HandleFunc("POST /api/lists", api.handleListsIndex)
	mux.HandleFunc("GET /api/lists/{id}", api.handleListByID)
	mux.HandleFunc("PUT /api/lists/{id}", api.handleListByID)
	mux.HandleFunc("DELETE /api/lists/{id}", api.handleListByID)
	mux.HandleFunc("GET /api/lists/{id}/posts", api.handleListPosts)
	mux.HandleFunc("POST /api/lists/{id}/posts/{postId}/favorite", api.handleListPostFavorite)
	mux.HandleFunc("POST /api/lists/{id}/subreddits", api.handleListSubreddits)
	mux.HandleFunc("DELETE /api/lists/{id}/subreddits/{name}", api.handleListSubredditByName)
	mux.HandleFunc("POST /api/lists/{id}/flickr-groups", api.handleListFlickrGroups)
	mux.HandleFunc("DELETE /api/lists/{id}/flickr-groups/{name}", api.handleListFlickrGroupByName)
	mux.HandleFunc("POST /api/lists/{id}/lemmy-communities", api.handleListLemmyCommunities)
	mux.HandleFunc("DELETE /api/lists/{id}/lemmy-communities/{name}", api.handleListLemmyCommunityByName)
	mux.HandleFunc("POST /api/lists/{id}/refresh", api.handleListRefresh)
	mux.HandleFunc("GET /api/lists/{id}/status", api.handleListStatus)

	// Global config
	mux.HandleFunc("/api/config", api.handleConfig)

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

// ---- Lists ----

func (api *APIServer) handleListsIndex(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		lists := api.config.GetLists()
		result := make([]map[string]interface{}, 0, len(lists))
		for _, l := range lists {
			result = append(result, map[string]interface{}{
				"id":                l.ID,
				"name":              l.Name,
				"subreddits":        l.Subreddits,
				"flickr_groups":     l.FlickrGroups,
				"lemmy_communities": l.LemmyCommunities,
				"post_count":        len(api.storage.GetPosts(l.ID)),
				"favorite_count":    len(api.storage.GetFavorites(l.ID)),
			})
		}
		apiSuccess(w, result)

	case http.MethodPost:
		var req struct {
			Name             string   `json:"name"`
			Subreddits       []string `json:"subreddits"`
			FlickrGroups     []string `json:"flickr_groups"`
			LemmyCommunities []string `json:"lemmy_communities"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			apiError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		list, err := api.config.AddList(NewListInput{
			Name:             req.Name,
			Subreddits:       req.Subreddits,
			FlickrGroups:     req.FlickrGroups,
			LemmyCommunities: req.LemmyCommunities,
		})
		if err != nil {
			apiError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := api.config.Save(api.configPath); err != nil {
			apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
			return
		}

		if len(list.Subreddits) > 0 || len(list.FlickrGroups) > 0 || len(list.LemmyCommunities) > 0 {
			select {
			case api.refreshCh <- struct{}{}:
			default:
			}
		}

		log.Printf("List created: %q (%s)", list.Name, list.ID)
		apiSuccess(w, list)

	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (api *APIServer) handleListByID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	list, ok := api.config.GetList(id)
	if !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		apiSuccess(w, list)

	case http.MethodPut:
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			apiError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !api.config.RenameList(id, req.Name) {
			apiError(w, http.StatusBadRequest, "name is required")
			return
		}
		if err := api.config.Save(api.configPath); err != nil {
			apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
			return
		}
		updated, _ := api.config.GetList(id)
		apiSuccess(w, updated)

	case http.MethodDelete:
		api.config.RemoveList(id)
		if err := api.config.Save(api.configPath); err != nil {
			apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
			return
		}
		if err := api.storage.RemoveList(id); err != nil {
			log.Printf("Warning: failed to remove data for list %s: %v", id, err)
		}
		log.Printf("List deleted: %q (%s)", list.Name, id)
		apiSuccess(w, map[string]string{"id": id})

	default:
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ---- Posts ----

func (api *APIServer) handleListPosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}

	posts := api.storage.GetPosts(id)
	favs := api.storage.GetFavorites(id)

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

func (api *APIServer) handleListPostFavorite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}
	postID := r.PathValue("postId")

	nowFav, err := api.storage.ToggleFavorite(id, postID)
	if err != nil {
		apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to toggle favorite: %v", err))
		return
	}

	// When newly favorited, download media asynchronously.
	if nowFav {
		posts := api.storage.GetPosts(id)
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

// ---- Config (global settings) ----

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
			"imgur_client_id":   api.config.ImgurClientID,
			"flickr_api_key":    api.config.FlickrAPIKey,
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
		if v, ok := updates["flickr_api_key"].(string); ok {
			api.config.FlickrAPIKey = v
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

// ---- Subreddits (per list) ----

func (api *APIServer) handleListSubreddits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}

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

	added, _ := api.config.AddSubredditToList(id, name)
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

	log.Printf("Subreddit added to list %s: r/%s", id, name)
	apiSuccess(w, map[string]interface{}{"name": name, "added": added})
}

func (api *APIServer) handleListSubredditByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}
	name := strings.ToLower(r.PathValue("name"))

	api.config.RemoveSubredditFromList(id, name)
	if err := api.config.Save(api.configPath); err != nil {
		apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
		return
	}

	// Remove non-favorited posts for this subreddit.
	if err := api.storage.RemoveSourceData(id, SourceReddit, name); err != nil {
		log.Printf("Warning: failed to remove data for r/%s in list %s: %v", name, id, err)
	}

	log.Printf("Subreddit removed from list %s: r/%s", id, name)
	apiSuccess(w, map[string]string{"name": name})
}

// ---- Flickr groups (per list) ----

func (api *APIServer) handleListFlickrGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := normalizeFlickrGroupSlug(req.Name)
	if name == "" {
		apiError(w, http.StatusBadRequest, "name is required")
		return
	}

	added, _ := api.config.AddFlickrGroupToList(id, name)
	if err := api.config.Save(api.configPath); err != nil {
		apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
		return
	}

	if added {
		select {
		case api.refreshCh <- struct{}{}:
		default:
		}
	}

	log.Printf("Flickr group added to list %s: %s", id, name)
	apiSuccess(w, map[string]interface{}{"name": name, "added": added})
}

func (api *APIServer) handleListFlickrGroupByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}
	name := r.PathValue("name")

	api.config.RemoveFlickrGroupFromList(id, name)
	if err := api.config.Save(api.configPath); err != nil {
		apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
		return
	}

	if err := api.storage.RemoveSourceData(id, SourceFlickr, name); err != nil {
		log.Printf("Warning: failed to remove data for flickr group %s in list %s: %v", name, id, err)
	}

	log.Printf("Flickr group removed from list %s: %s", id, name)
	apiSuccess(w, map[string]string{"name": name})
}

// ---- Lemmy communities (per list) ----

func (api *APIServer) handleListLemmyCommunities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := normalizeLemmyIdentifier(req.Name)
	if name == "" {
		apiError(w, http.StatusBadRequest, `name must look like "community@instance"`)
		return
	}

	added, _ := api.config.AddLemmyCommunityToList(id, name)
	if err := api.config.Save(api.configPath); err != nil {
		apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
		return
	}

	if added {
		select {
		case api.refreshCh <- struct{}{}:
		default:
		}
	}

	log.Printf("Lemmy community added to list %s: %s", id, name)
	apiSuccess(w, map[string]interface{}{"name": name, "added": added})
}

func (api *APIServer) handleListLemmyCommunityByName(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}
	name := strings.ToLower(r.PathValue("name"))

	api.config.RemoveLemmyCommunityFromList(id, name)
	if err := api.config.Save(api.configPath); err != nil {
		apiError(w, http.StatusInternalServerError, fmt.Sprintf("failed to save config: %v", err))
		return
	}

	if err := api.storage.RemoveSourceData(id, SourceLemmy, name); err != nil {
		log.Printf("Warning: failed to remove data for lemmy community %s in list %s: %v", name, id, err)
	}

	log.Printf("Lemmy community removed from list %s: %s", id, name)
	apiSuccess(w, map[string]string{"name": name})
}

// ---- Refresh ----

func (api *APIServer) handleListRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	if _, ok := api.config.GetList(id); !ok {
		apiError(w, http.StatusNotFound, "list not found")
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

func (api *APIServer) handleListStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	id := r.PathValue("id")
	list, ok := api.config.GetList(id)
	if !ok {
		apiError(w, http.StatusNotFound, "list not found")
		return
	}

	posts := api.storage.GetPosts(id)
	favs := api.storage.GetFavorites(id)

	apiSuccess(w, map[string]interface{}{
		"posts_count":       len(posts),
		"favorites_count":   len(favs),
		"subreddits":        list.Subreddits,
		"flickr_groups":     list.FlickrGroups,
		"lemmy_communities": list.LemmyCommunities,
		"last_checked": map[string]interface{}{
			"reddit": lastCheckedMap(api.storage, id, SourceReddit, list.Subreddits),
			"flickr": lastCheckedMap(api.storage, id, SourceFlickr, list.FlickrGroups),
			"lemmy":  lastCheckedMap(api.storage, id, SourceLemmy, list.LemmyCommunities),
		},
	})
}

// lastCheckedMap builds a name -> "never"|RFC3339 map for one source's identifiers.
func lastCheckedMap(storage *Storage, listID string, source PostSource, names []string) map[string]string {
	out := make(map[string]string, len(names))
	for _, name := range names {
		t := storage.GetLastChecked(listID, source, name)
		if t.IsZero() {
			out[name] = "never"
		} else {
			out[name] = t.Format(time.RFC3339)
		}
	}
	return out
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
