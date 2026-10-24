package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	transportTTL     = 2 * time.Minute
	transportLiveTTL = 20 * time.Second
)

type TransportSource struct {
	apiURL string
	apiKey string
	stops  []stopInfo
	ready  bool

	// Live requests use shorter TTL but share the cache
	mu    sync.RWMutex
	cache map[int]*departureCache
}

type stopInfo struct {
	line      string
	name      string
	stopRef   string
	color     string
	colorText string
}

type departureCache struct {
	destinations []Destination
	fetchedAt    time.Time
}

// API response
type TransportData struct {
	Stops []StopData `json:"stops"`
}

type StopData struct {
	ID           int           `json:"id"`
	Name         string        `json:"name"`
	Line         string        `json:"line"`
	Color        string        `json:"color"`
	ColorText    string        `json:"color_text"`
	Destinations []Destination `json:"destinations"`
}

type Destination struct {
	Name       string      `json:"name"`
	Departures []Departure `json:"departures"`
}

type Departure struct {
	Time     string `json:"time"`
	Realtime bool   `json:"realtime"`
}

func NewTransportSource(cfg *Config) *TransportSource {
	s := &TransportSource{
		apiURL: cfg.TransportAPIURL,
		apiKey: cfg.TransportAPIKey,
		cache:  make(map[int]*departureCache),
	}

	// Parse config into temporary resolution data
	for _, entry := range strings.Split(cfg.TransportStops, ";") {
		parts := strings.SplitN(strings.TrimSpace(entry), ",", 3)
		if len(parts) == 3 {
			s.stops = append(s.stops, stopInfo{
				line:    parts[0],
				name:    parts[1],
				stopRef: parts[2],
			})
		} else if entry != "" {
			log.Printf("[transport] invalid stop config: %q", entry)
		}
	}
	return s
}

func (s *TransportSource) Name() string               { return "transport" }
func (s *TransportSource) DegradedTTL() time.Duration { return time.Hour }

func (s *TransportSource) Fetch() *Response {
	if s.apiKey == "" {
		return ErrorResponse("transport not configured", time.Hour)
	}
	if len(s.stops) == 0 {
		return ErrorResponse("no stops configured", time.Hour)
	}
	if !s.ready {
		if err := s.resolveStops(); err != nil {
			return ErrorResponse("resolve: "+err.Error(), time.Hour)
		}
	}

	var stops []StopData
	for i := range s.stops {
		if s.stops[i].stopRef == "" {
			continue
		}
		data := s.getStopData(i, transportTTL)
		if data != nil {
			stops = append(stops, *data)
		}
	}

	if len(stops) == 0 {
		return ErrorResponse("no departure data", 5*time.Minute)
	}
	return NewResponse(TransportData{Stops: stops}, transportTTL)
}

func (s *TransportSource) FetchLive(id int) *Response {
	if s.apiKey == "" {
		return ErrorResponse("transport not configured", time.Hour)
	}
	if id < 0 || id >= len(s.stops) || !s.ready || s.stops[id].stopRef == "" {
		return ErrorResponse("invalid stop", time.Minute)
	}

	data := s.getStopData(id, transportLiveTTL)
	if data == nil {
		return ErrorResponse("fetch failed", time.Minute)
	}
	return NewResponse(data, transportLiveTTL)
}

// Build StopData from static info and cached departures
func (s *TransportSource) getStopData(id int, maxAge time.Duration) *StopData {
	destinations, err := s.getDepartures(id, maxAge)
	if err != nil {
		log.Printf("[transport] stop %s %s: %v", s.stops[id].line, s.stops[id].name, err)
		return nil
	}

	stop := &s.stops[id]
	return &StopData{
		ID:           id,
		Name:         stop.name,
		Line:         stop.line,
		Color:        stop.color,
		ColorText:    stop.colorText,
		Destinations: destinations,
	}
}

// Get departures with caching
func (s *TransportSource) getDepartures(id int, maxAge time.Duration) ([]Destination, error) {
	s.mu.RLock()
	cached := s.cache[id]
	s.mu.RUnlock()

	if cached != nil && time.Since(cached.fetchedAt) < maxAge {
		return cached.destinations, nil
	}

	destinations, err := s.fetchDepartures(id)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cache[id] = &departureCache{destinations: destinations, fetchedAt: time.Now()}
	s.mu.Unlock()

	return destinations, nil
}

// Fetch departures from CTS
func (s *TransportSource) fetchDepartures(id int) ([]Destination, error) {
	stop := &s.stops[id]

	var resp struct {
		ServiceDelivery struct {
			StopMonitoringDelivery []struct {
				MonitoredStopVisit []struct {
					MonitoredVehicleJourney struct {
						DestinationName string
						MonitoredCall   struct {
							ExpectedDepartureTime string
							Extension             struct{ IsRealTime bool }
						}
					}
				}
			}
		}
	}

	query := url.Values{
		"LineRef":                  {stop.line},
		"MonitoringRef":            {stop.stopRef},
		"MinimumStopVisitsPerLine": {"4"},
	}
	headers := http.Header{"Authorization": {"Basic " + s.apiKey}}
	if _, err := GetJSON(s.apiURL+"/stop-monitoring", query, headers, nil, &resp, checkErrCTS); err != nil {
		return nil, err
	}

	// Group departures by destination
	byDest := make(map[string][]Departure)
	if len(resp.ServiceDelivery.StopMonitoringDelivery) > 0 {
		for _, visit := range resp.ServiceDelivery.StopMonitoringDelivery[0].MonitoredStopVisit {
			journey := visit.MonitoredVehicleJourney
			depTime, err := time.Parse(time.RFC3339, journey.MonitoredCall.ExpectedDepartureTime)
			// TODO: check if depTime.Before(time.Now()) condition is necessary
			if err != nil {
				continue
			}
			dest := journey.DestinationName
			byDest[dest] = append(byDest[dest], Departure{
				Time:     depTime.Format(time.RFC3339),
				Realtime: journey.MonitoredCall.Extension.IsRealTime,
			})
		}
	}

	destinations := make([]Destination, 0, len(byDest))
	for name, deps := range byDest {
		destinations = append(destinations, Destination{Name: name, Departures: deps})
	}
	return destinations, nil
}

// Stop reference from API response
type AnnotatedStopPointRef struct {
	StopPointRef string
	StopName     string
	Lines        []struct {
		LineRef      string
		Destinations []struct{ DestinationName []string }
		Extension    struct{ RouteColor, RouteTextColor string }
	}
}

// Resolve stop references defined in config
func (s *TransportSource) resolveStops() error {
	var resp struct {
		StopPointsDelivery struct {
			AnnotatedStopPointRef []AnnotatedStopPointRef
		}
	}

	query := url.Values{"includeLinesDestinations": {"true"}}
	headers := http.Header{"Authorization": {"Basic " + s.apiKey}}
	if _, err := GetJSON(s.apiURL+"/stoppoints-discovery", query, headers, nil, &resp, checkErrCTS); err != nil {
		return err
	}

	for i := range s.stops {
		// Destination stored in stopRef until resolution
		destination := s.stops[i].stopRef
		s.stops[i].stopRef = ""
		s.resolveStop(i, destination, resp.StopPointsDelivery.AnnotatedStopPointRef)
	}
	s.ready = true
	return nil
}

// Resolve a single stop using API data
func (s *TransportSource) resolveStop(id int, destination string, apiStops []AnnotatedStopPointRef) {
	stop := &s.stops[id]
	for _, as := range apiStops {
		if !strings.Contains(as.StopName, stop.name) {
			continue
		}
		for _, line := range as.Lines {
			if line.LineRef != stop.line {
				continue
			}
			for _, dest := range line.Destinations {
				for _, name := range dest.DestinationName {
					if strings.Contains(name, destination) {
						stop.stopRef = as.StopPointRef
						stop.color = "#" + line.Extension.RouteColor
						stop.colorText = "#" + line.Extension.RouteTextColor
						log.Printf("[transport] resolved %s/%s -> %s", stop.line, stop.name, stop.stopRef)
						return
					}
				}
			}
		}
	}
	log.Printf("[transport] unresolved %s/%s/%s", stop.line, stop.name, destination)
}

// Check for error in CTS response
func checkErrCTS(body []byte) error {
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}
