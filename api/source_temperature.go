package main

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"sync"
	"time"
)

const temperatureBufSize = 5

// TemperatureSource receives push data from the sensor and serves
// the median of the last few readings.
type TemperatureSource struct {
	mu       sync.RWMutex
	temps    []float64
	hums     []float64
	location string
	lastPush time.Time
}

// TemperaturePayload is the JSON body sent by the sensor.
type TemperaturePayload struct {
	Temperature float64 `json:"temperature"`
	Humidity    float64 `json:"humidity"`
	Location    string  `json:"location"`
}

// TemperatureData is returned in API responses.
type TemperatureData struct {
	Temperature float64 `json:"temperature"`
	Humidity    float64 `json:"humidity"`
	Location    string  `json:"location"`
}

func NewTemperatureSource(_ *Config) *TemperatureSource {
	return &TemperatureSource{}
}

func (s *TemperatureSource) Name() string               { return "temperature" }
func (s *TemperatureSource) DegradedTTL() time.Duration { return 4 * time.Hour }

// Fetch returns the median reading or an error if no data is available.
func (s *TemperatureSource) Fetch() *Response {
	s.mu.RLock()
	defer s.mu.RUnlock()

	n := len(s.temps)
	if n == 0 {
		return ErrorResponse("no sensor data", 30*time.Second)
	}

	return NewResponse(TemperatureData{
		Temperature: median(s.temps),
		Humidity:    median(s.hums),
		Location:    s.location,
	}, 30*time.Second)
}

// HandlePush returns an HTTP handler that accepts POST data from the sensor.
func (s *TemperatureSource) HandlePush() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var p TemperaturePayload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		s.push(p)
		log.Printf("temperature: received %.1f °C, %.1f %% (%s)", p.Temperature, p.Humidity, p.Location)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *TemperatureSource) push(p TemperaturePayload) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.temps = append(s.temps, p.Temperature)
	s.hums = append(s.hums, p.Humidity)
	if len(s.temps) > temperatureBufSize {
		s.temps = s.temps[1:]
		s.hums = s.hums[1:]
	}
	s.location = p.Location
	s.lastPush = time.Now()
}

// median returns the median of a float64 slice. The slice must not be empty.
func median(vals []float64) float64 {
	sorted := slices.Clone(vals)
	slices.Sort(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
