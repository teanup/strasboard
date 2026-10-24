/*
StrasBoard Server
Aggregates data from multiple sources and serves a dashboard.
*/
package main

import (
	"embed"
	"encoding/json"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

//go:embed templates/*
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

type AllData struct {
	Weather     *Response `json:"weather"`
	Transport   *Response `json:"transport"`
	Temperature *Response `json:"temperature"`
	Electricity *Response `json:"electricity"`
	Tempo       *Response `json:"tempo"`
	Timestamp   string    `json:"timestamp"`
}

// Main entry point
func main() {
	godotenv.Load()

	cfg := LoadConfig()
	cache := NewCache()

	// Initialize sources
	sources := map[string]Source{
		"weather":     NewWeatherSource(cfg),
		"transport":   NewTransportSource(cfg),
		"temperature": NewTemperatureSource(cfg),
		"electricity": NewElectricitySource(cfg),
		"tempo":       NewTempoSource(cfg),
	}

	mux := http.NewServeMux()

	// Static files
	mux.Handle("/static/", http.FileServer(http.FS(staticFS)))

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{
			"status":    "ok",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Sensor push endpoint
	mux.HandleFunc("/api/temperature/push", sources["temperature"].(*TemperatureSource).HandlePush())

	// Individual endpoints
	for name, src := range sources {
		mux.HandleFunc("/api/"+name, sourceHandler(src, cache))
	}

	// Transport live endpoint
	mux.HandleFunc("/api/transport/live", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.URL.Query().Get("id")
		id, err := strconv.Atoi(idStr)
		if err != nil {
			writeJSON(w, ErrorResponse("invalid id", time.Minute))
			return
		}
		transport := sources["transport"].(*TransportSource)
		writeJSON(w, transport.FetchLive(id))
	})

	// All data combined
	mux.HandleFunc("/api/all", func(w http.ResponseWriter, r *http.Request) {
		data := fetchAll(cache, sources)
		writeJSON(w, data)
	})

	// HTML dashboard
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		tmpl, err := template.ParseFS(templatesFS, "templates/dashboard.html")
		if err != nil {
			log.Printf("Template parse error: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if err := tmpl.Execute(w, nil); err != nil {
			log.Printf("Template execute error: %v", err)
		}
	})

	// Pre-warm cache
	go fetchAll(cache, sources)

	log.Printf("StrasBoard server starting on :%s", cfg.Port)
	log.Fatal(http.ListenAndServe(":"+cfg.Port, mux))
}

// Create HTTP handler for a source
func sourceHandler(src Source, cache *Cache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := fetchCached(cache, src)
		writeJSON(w, data)
	}
}

// Fetch source data with caching and degraded mode
func fetchCached(cache *Cache, src Source) *Response {
	if cached := cache.Get(src.Name()); cached != nil {
		return cached
	}

	resp := src.Fetch()
	if resp.Error != "" {
		if backup := cache.GetBackup(src.Name()); backup != nil {
			resp = DegradedResponse(backup, resp)
		}
	}

	cache.Set(src.Name(), resp, src.DegradedTTL())
	return resp
}

// Fetch all sources concurrently
func fetchAll(cache *Cache, sources map[string]Source) *AllData {
	results := make(map[string]*Response)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for name, src := range sources {
		wg.Add(1)
		go func(n string, s Source) {
			defer wg.Done()
			resp := fetchCached(cache, s)
			mu.Lock()
			results[n] = resp
			mu.Unlock()
		}(name, src)
	}
	wg.Wait()

	return &AllData{
		Weather:     results["weather"],
		Transport:   results["transport"],
		Temperature: results["temperature"],
		Electricity: results["electricity"],
		Tempo:       results["tempo"],
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
}

// Write JSON response
func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
