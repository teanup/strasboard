// Package transport fetches live departures from the CTS network.
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"strasboard/api/config"
	"strasboard/api/httpx"
)

// apiURL is the CTS SIRI endpoint.
const apiURL = "https://api.cts-strasbourg.eu/v1/siri/2.0"

// refreshInterval is how often the job re-fetches departures.
const refreshInterval = 2 * time.Minute

// ResourceName is the store resource holding transport stops.
const ResourceName = "transport"

// Source fetches departures for the configured stops.
type Source struct {
	apiKey string
	stops  []stopInfo

	mu    sync.Mutex
	ready bool
}

type stopInfo struct {
	line      string
	name      string
	stopRef   string
	color     string
	colorText string
}

// Stop is the payload stored for a single stop.
type Stop struct {
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

// New returns a transport Source configured from cfg.
func New(cfg *config.Config) *Source {
	s := &Source{
		apiKey: cfg.Transport.APIKey,
	}

	// The third field is the destination until stops are resolved.
	for entry := range strings.SplitSeq(cfg.Transport.Stops, ";") {
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

func (s *Source) Name() string { return "transport" }

// NextRefresh returns the fixed refresh interval.
func (s *Source) NextRefresh(now time.Time) time.Time {
	return now.Add(refreshInterval)
}

// Refresh resolves the configured stops (once) and fetches departures for all
// of them, returning one item per stop keyed by stop ID.
func (s *Source) Refresh(_ context.Context) (map[string]any, error) {
	if s.apiKey == "" {
		return nil, fmt.Errorf("transport not configured")
	}
	if len(s.stops) == 0 {
		return nil, fmt.Errorf("no stops configured")
	}
	if err := s.ensureResolved(); err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}

	items := make(map[string]any)
	for i := range s.stops {
		if s.stops[i].stopRef == "" {
			continue
		}
		stop, err := s.fetchStop(i)
		if err != nil {
			log.Printf("[transport] stop %s %s: %v", s.stops[i].line, s.stops[i].name, err)
			continue
		}
		items[strconv.Itoa(i)] = stop
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("no departure data")
	}
	return items, nil
}

// RefreshStop re-fetches a single stop and returns its item. It is used by the
// live endpoint to refresh one stop on demand.
func (s *Source) RefreshStop(id int) (Stop, error) {
	if s.apiKey == "" {
		return Stop{}, fmt.Errorf("transport not configured")
	}
	if err := s.ensureResolved(); err != nil {
		return Stop{}, fmt.Errorf("resolve: %w", err)
	}
	if id < 0 || id >= len(s.stops) || s.stops[id].stopRef == "" {
		return Stop{}, fmt.Errorf("invalid stop")
	}
	return s.fetchStop(id)
}

// ensureResolved resolves stop references once, on first use.
func (s *Source) ensureResolved() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ready {
		return nil
	}
	if err := s.resolveStops(); err != nil {
		return err
	}
	s.ready = true
	return nil
}

// fetchStop builds a Stop from static info and freshly fetched departures.
func (s *Source) fetchStop(id int) (Stop, error) {
	destinations, err := s.fetchDepartures(id)
	if err != nil {
		return Stop{}, err
	}
	stop := &s.stops[id]
	return Stop{
		ID:           id,
		Name:         stop.name,
		Line:         stop.line,
		Color:        stop.color,
		ColorText:    stop.colorText,
		Destinations: destinations,
	}, nil
}

// fetchDepartures fetches and groups departures by destination.
func (s *Source) fetchDepartures(id int) ([]Destination, error) {
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
	if _, err := httpx.GetJSON(apiURL+"/stop-monitoring", query, headers, nil, &resp, checkErrCTS); err != nil {
		return nil, err
	}

	byDest := make(map[string][]Departure)
	if len(resp.ServiceDelivery.StopMonitoringDelivery) > 0 {
		for _, visit := range resp.ServiceDelivery.StopMonitoringDelivery[0].MonitoredStopVisit {
			journey := visit.MonitoredVehicleJourney
			depTime, err := time.Parse(time.RFC3339, journey.MonitoredCall.ExpectedDepartureTime)
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

// annotatedStopPointRef is a stop reference from the discovery API.
type annotatedStopPointRef struct {
	StopPointRef string
	StopName     string
	Lines        []struct {
		LineRef      string
		Destinations []struct{ DestinationName []string }
		Extension    struct{ RouteColor, RouteTextColor string }
	}
}

// resolveStops resolves the stop references defined in config.
func (s *Source) resolveStops() error {
	var resp struct {
		StopPointsDelivery struct {
			AnnotatedStopPointRef []annotatedStopPointRef
		}
	}

	query := url.Values{"includeLinesDestinations": {"true"}}
	headers := http.Header{"Authorization": {"Basic " + s.apiKey}}
	if _, err := httpx.GetJSON(apiURL+"/stoppoints-discovery", query, headers, nil, &resp, checkErrCTS); err != nil {
		return err
	}

	for i := range s.stops {
		destination := s.stops[i].stopRef
		s.stops[i].stopRef = ""
		s.resolveStop(i, destination, resp.StopPointsDelivery.AnnotatedStopPointRef)
	}
	return nil
}

// resolveStop resolves a single stop using discovery API data.
func (s *Source) resolveStop(id int, destination string, apiStops []annotatedStopPointRef) {
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

// checkErrCTS returns an error if the CTS response reports one.
func checkErrCTS(body []byte) error {
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && resp.Error != "" {
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}
