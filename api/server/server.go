// Package server wires the HTTP API: routing, handlers and middleware.
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"strasboard/api/config"
	"strasboard/api/source/temperature"
	"strasboard/api/source/transport"
	"strasboard/api/store"
)

// Server holds the HTTP dependencies shared by all handlers.
type Server struct {
	cfg       *config.Config
	store     *store.Store
	transport *transport.Source
}

// New builds a Server from the given config, store and transport source.
// The transport source is used by the live-refresh endpoint; it may be nil.
func New(cfg *config.Config, st *store.Store, tr *transport.Source) *Server {
	return &Server{cfg: cfg, store: st, transport: tr}
}

// Handler returns the root HTTP handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/all", s.handleAll)

	// Derived endpoints must be registered before the generic {id} route.
	mux.HandleFunc("GET /api/v1/temperature/current", s.handleTemperatureCurrent)
	mux.HandleFunc("GET /api/v1/transport/live", s.handleTransportLive)

	// Generic CRUD over every resource.
	mux.HandleFunc("GET /api/v1/{resource}", s.handleList)
	mux.HandleFunc("POST /api/v1/{resource}", s.handleCreate)
	mux.HandleFunc("GET /api/v1/{resource}/{id}", s.handleGet)
	mux.HandleFunc("PUT /api/v1/{resource}/{id}", s.handlePut)
	mux.HandleFunc("DELETE /api/v1/{resource}/{id}", s.handleDelete)

	return s.withCORS(mux)
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
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// handleAll returns every resource in a single payload, keyed by resource name.
func (s *Server) handleAll(w http.ResponseWriter, _ *http.Request) {
	result := make(map[string]any)
	for _, name := range s.store.Resources() {
		items, meta, _ := s.store.List(name)
		result[name] = map[string]any{
			"items":      items,
			"updated_at": meta.UpdatedAt,
			"error":      meta.Error,
		}
	}
	result["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, result)
}

// handleTemperatureCurrent returns the aggregated current indoor reading,
// derived from the most recent stored readings.
func (s *Server) handleTemperatureCurrent(w http.ResponseWriter, _ *http.Request) {
	items, _, ok := s.store.List(temperature.ResourceName)
	if !ok {
		writeError(w, http.StatusNotFound, "no sensor data")
		return
	}

	readings := make([]temperature.Reading, 0, len(items))
	for _, item := range items {
		// Items may be stored as typed values (from a snapshot) or as generic
		// maps (from a JSON request body), so decode through JSON to normalize.
		var r temperature.Reading
		if err := decodeInto(item.Data, &r); err != nil {
			continue
		}
		readings = append(readings, r)
	}

	current, ok := temperature.Aggregate(readings)
	if !ok {
		writeError(w, http.StatusNotFound, "no sensor data")
		return
	}
	writeJSON(w, http.StatusOK, current)
}

// decodeInto re-encodes v and decodes it into dest, normalizing between typed
// values and generic JSON maps.
func decodeInto(v any, dest any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

// handleTransportLive re-fetches a single stop on demand and updates the store.
func (s *Server) handleTransportLive(w http.ResponseWriter, r *http.Request) {
	if s.transport == nil {
		writeError(w, http.StatusNotFound, "transport not configured")
		return
	}

	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	stop, err := s.transport.RefreshStop(id)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	s.store.Set(transport.ResourceName, strconv.Itoa(id), stop)
	writeJSON(w, http.StatusOK, stop)
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
		"error":      meta.Error,
	})
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
	item, _ := s.store.Get(name, id)
	writeJSON(w, http.StatusOK, item)
}

// handleDelete removes an item.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.store.Delete(r.PathValue("resource"), r.PathValue("id")) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
