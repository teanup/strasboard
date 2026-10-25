// Package server wires the HTTP API: routing, handlers and middleware.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"strasboard/api/config"
	"strasboard/api/jobs"
	"strasboard/api/source"
	"strasboard/api/source/temperature"
	"strasboard/api/store"
)

// Server holds the HTTP dependencies shared by all handlers.
type Server struct {
	cfg      *config.Config
	store    *store.Store
	schedule *jobs.Schedule
	// refreshers maps a resource name to its on-demand item refresher, when
	// the source implements source.ItemRefresher.
	refreshers map[string]source.ItemRefresher
}

// New builds a Server from the given config, store, schedule and resources.
// Resources that implement source.ItemRefresher gain the
// POST /api/v1/{resource}/{id}/refresh endpoint.
//
// Every resource is pre-registered in the store so that reads of a
// not-yet-populated resource return an empty collection rather than 404.
func New(cfg *config.Config, st *store.Store, schedule *jobs.Schedule, resources ...source.Resource) *Server {
	refreshers := make(map[string]source.ItemRefresher)
	for _, r := range resources {
		st.Ensure(r.Name())
		if ir, ok := r.(source.ItemRefresher); ok {
			refreshers[r.Name()] = ir
		}
	}
	// Temperature is pushed by the sensor, not fetched by a job, so it has no
	// source in the resources list.
	st.Ensure(temperature.ResourceName)

	return &Server{cfg: cfg, store: st, schedule: schedule, refreshers: refreshers}
}

// Handler returns the root HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)

	// Generic REST CRUD over every resource.
	mux.HandleFunc("GET /api/v1/{resource}", s.handleList)
	mux.HandleFunc("POST /api/v1/{resource}", s.handleCreate)
	mux.HandleFunc("GET /api/v1/{resource}/{id}", s.handleGet)
	mux.HandleFunc("PUT /api/v1/{resource}/{id}", s.handlePut)
	mux.HandleFunc("DELETE /api/v1/{resource}/{id}", s.handleDelete)

	// On-demand refresh of a single item, for resources that support it.
	mux.HandleFunc("POST /api/v1/{resource}/{id}/refresh", s.handleRefresh)

	return s.withCORS(s.withCache(mux))
}

// withCORS applies the configured CORS policy to every response.
func (s *Server) withCORS(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(s.cfg.CORS.AllowedOrigins))
	for _, o := range s.cfg.CORS.AllowedOrigins {
		allowed[o] = true
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			// Expose validators so cross-origin clients can use conditional
			// requests and read the freshness window.
			w.Header().Set("Access-Control-Expose-Headers", "ETag, Last-Modified, Cache-Control")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withCache sets Cache-Control, ETag and Last-Modified on successful GET
// responses, and answers conditional requests with 304 Not Modified.
//
// The freshness window comes from the resource's NextRefresh schedule: the
// server already knows when the data will change, so it tells clients exactly
// how long they can reuse it without re-fetching.
//
// It runs before the mux, so it extracts the resource name from the URL path
// rather than relying on r.PathValue (which is only populated after routing).
func (s *Server) withCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}

		name := resourceFromPath(r.URL.Path)
		if name == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Only cache reads of resources we actually know about.
		meta, ok := s.store.Meta(name)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		now := time.Now()
		etag := etagFor(meta.UpdatedAt)
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", meta.UpdatedAt.UTC().Format(http.TimeFormat))

		if match := r.Header.Get("If-None-Match"); match != "" && match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if since := r.Header.Get("If-Modified-Since"); since != "" && !meta.UpdatedAt.IsZero() {
			if t, err := http.ParseTime(since); err == nil && !meta.UpdatedAt.After(t) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}

		// A degraded resource may recover at any moment, so do not let clients
		// cache it for long.
		maxAge := s.schedule.MaxAge(name, now)
		if meta.Error != "" {
			maxAge = 0
		}
		w.Header().Set("Cache-Control", cacheControl(maxAge))

		next.ServeHTTP(w, r)
	})
}

// resourceFromPath extracts the resource name from an /api/v1/{resource}...
// path. It returns "" for any other path.
func resourceFromPath(path string) string {
	const prefix = "/api/v1/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := path[len(prefix):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// cacheControl renders a Cache-Control value for a freshness window. Windows
// shorter than a second are rounded up to one second so clients still get a
// small reuse benefit; the value is always floored to whole seconds.
func cacheControl(maxAge time.Duration) string {
	secs := max(int(maxAge/time.Second), 1)
	return fmt.Sprintf("public, max-age=%d", secs)
}

// etagFor derives a weak ETag from a resource's last update time.
func etagFor(updatedAt time.Time) string {
	return fmt.Sprintf(`W/"%d"`, updatedAt.UnixNano())
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// handleList returns all items of a resource.
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("resource")
	items, meta, ok := s.store.List(name)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown resource")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":      items,
		"updated_at": meta.UpdatedAt,
		"error":      s.resourceError(name, items, meta),
	})
}

// resourceError returns the error to report for a resource read. It combines
// the stored degraded-mode error with any time-based staleness policy.
func (s *Server) resourceError(name string, items []store.Item, meta store.Resource) string {
	if meta.Error != "" {
		return meta.Error
	}
	if name == temperature.ResourceName && len(items) > 0 {
		// Items are sorted by ID; the last one is the newest reading.
		newest := items[len(items)-1].UpdatedAt
		if temperature.Stale(newest, time.Now().UTC()) {
			return temperature.StaleError
		}
	}
	return ""
}

// handleGet returns a single item.
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	item, ok := s.store.Get(r.PathValue("resource"), r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// handleCreate stores a new item. The ID is taken from the request body's "id"
// field.
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("resource")

	var body struct {
		ID   string `json:"id"`
		Data any    `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.ID == "" {
		writeError(w, http.StatusBadRequest, "missing id")
		return
	}

	s.store.Set(name, body.ID, body.Data)
	s.applyRetention(name)
	item, _ := s.store.Get(name, body.ID)
	writeJSON(w, http.StatusCreated, item)
}

// handlePut replaces an existing item.
func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("resource"), r.PathValue("id")

	var data any
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	s.store.Set(name, id, data)
	s.applyRetention(name)
	item, _ := s.store.Get(name, id)
	writeJSON(w, http.StatusOK, item)
}

// applyRetention prunes a resource to its configured size after a write.
func (s *Server) applyRetention(name string) {
	if name == temperature.ResourceName {
		s.store.Prune(name, temperature.Window)
	}
}

// handleDelete removes an item.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.store.Delete(r.PathValue("resource"), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRefresh re-fetches a single item from its upstream source and stores
// the result. It is only available for resources that implement
// source.ItemRefresher.
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	name, id := r.PathValue("resource"), r.PathValue("id")

	refresher, ok := s.refreshers[name]
	if !ok {
		writeError(w, http.StatusMethodNotAllowed, "resource does not support refresh")
		return
	}

	data, err := refresher.RefreshItem(r.Context(), id)
	if err != nil {
		if errors.Is(err, source.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	s.store.Set(name, id, data)
	item, _ := s.store.Get(name, id)
	writeJSON(w, http.StatusOK, item)
}

// writeJSON encodes data as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("write response: %v", err)
	}
}

// writeError writes a JSON error payload.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
