// Package weather fetches the forecast from Open-Meteo.
package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"strasboard/api/config"
	"strasboard/api/httpx"
)

// apiURL is the Open-Meteo forecast endpoint.
const apiURL = "https://api.open-meteo.com/v1/forecast"

// Item IDs for the three forecast parts.
const (
	currentID = "current"
	hourlyID  = "hourly"
	dailyID   = "daily"
)

// Each part of the forecast changes at a different rate, so it is cached and
// refreshed independently.
const (
	currentTTL = 1 * time.Hour
	hourlyTTL  = 3 * time.Hour
	dailyTTL   = 6 * time.Hour
)

// Source fetches weather data from Open-Meteo.
type Source struct {
	lat string
	lon string
	tz  string
	loc *time.Location

	mu      sync.Mutex
	current *part[Current]
	hourly  *part[[]Hour]
	daily   *part[[]Day]
}

// part is a cached forecast section with its own expiry.
type part[T any] struct {
	data      T
	expiresAt time.Time
}

func (p *part[T]) valid(now time.Time) bool {
	return p != nil && now.Before(p.expiresAt)
}

// New returns a weather Source configured from cfg.
func New(cfg *config.Config) *Source {
	loc, _ := time.LoadLocation(cfg.Weather.Timezone)
	if loc == nil {
		loc = time.Local
	}
	return &Source{
		lat: fmt.Sprintf("%.4f", cfg.Weather.Latitude),
		lon: fmt.Sprintf("%.4f", cfg.Weather.Longitude),
		tz:  cfg.Weather.Timezone,
		loc: loc,
	}
}

func (s *Source) Name() string { return "weather" }

// NextRefresh returns the earliest expiry among the cached parts.
func (s *Source) NextRefresh(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := now.Add(currentTTL)
	for _, exp := range []time.Time{
		expiry(s.current, now),
		expiry(s.hourly, now),
		expiry(s.daily, now),
	} {
		if exp.Before(next) {
			next = exp
		}
	}
	return next
}

// expiry returns a part's expiry, or now when the part is not cached yet.
func expiry[T any](p *part[T], now time.Time) time.Time {
	if p == nil {
		return now
	}
	return p.expiresAt
}

// Current is the current-conditions item payload.
type Current struct {
	Time        string  `json:"time"`
	Temperature float64 `json:"temperature"`
	FeelsLike   float64 `json:"feels_like"`
	IsDay       bool    `json:"is_day"`
	Code        int     `json:"code"`
}

// Hour is a single hourly forecast entry.
type Hour struct {
	Time        string  `json:"time"`
	Temperature float64 `json:"temperature"`
	FeelsLike   float64 `json:"feels_like"`
	IsDay       bool    `json:"is_day"`
	Code        int     `json:"code"`
}

// Day is a single daily forecast entry.
type Day struct {
	Date    string  `json:"date"`
	TempMax float64 `json:"temp_max"`
	TempMin float64 `json:"temp_min"`
	Code    int     `json:"code"`
}

// Refresh fetches the forecast parts whose TTL has expired and returns one
// item per part: current, hourly and daily.
func (s *Source) Refresh(_ context.Context) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().In(s.loc)

	if !s.current.valid(now) {
		current, err := s.fetchCurrent()
		if err != nil {
			return nil, fmt.Errorf("current: %w", err)
		}
		s.current = &part[Current]{data: current, expiresAt: now.Add(currentTTL)}
	}
	if !s.hourly.valid(now) {
		hourly, err := s.fetchHourly()
		if err != nil {
			return nil, fmt.Errorf("hourly: %w", err)
		}
		s.hourly = &part[[]Hour]{data: hourly, expiresAt: now.Add(hourlyTTL)}
	}
	if !s.daily.valid(now) {
		daily, err := s.fetchDaily()
		if err != nil {
			return nil, fmt.Errorf("daily: %w", err)
		}
		s.daily = &part[[]Day]{data: daily, expiresAt: now.Add(dailyTTL)}
	}

	return map[string]any{
		currentID: s.current.data,
		hourlyID:  filterHourly(s.hourly.data, s.loc),
		dailyID:   filterDaily(s.daily.data, s.loc),
	}, nil
}

// fetchCurrent fetches 15-minutely data and picks the slot closest to now.
func (s *Source) fetchCurrent() (Current, error) {
	var resp struct {
		Minutely15 struct {
			Time        []string  `json:"time"`
			Temp        []float64 `json:"temperature_2m"`
			FeelsLike   []float64 `json:"apparent_temperature"`
			IsDay       []int     `json:"is_day"`
			WeatherCode []int     `json:"weather_code"`
		} `json:"minutely_15"`
	}

	query := url.Values{
		"models":               {"meteofrance_seamless"},
		"minutely_15":          {"temperature_2m,apparent_temperature,is_day,weather_code"},
		"forecast_minutely_15": {"8"},
		"latitude":             {s.lat},
		"longitude":            {s.lon},
		"timezone":             {s.tz},
	}
	if _, err := httpx.GetJSON(apiURL, query, nil, nil, &resp, checkErrOpenMeteo); err != nil {
		return Current{}, err
	}
	if len(resp.Minutely15.Time) == 0 {
		return Current{}, fmt.Errorf("no data")
	}

	slots := make([]Current, len(resp.Minutely15.Time))
	for i, t := range resp.Minutely15.Time {
		slots[i] = Current{
			Time:        t,
			Temperature: resp.Minutely15.Temp[i],
			FeelsLike:   resp.Minutely15.FeelsLike[i],
			IsDay:       resp.Minutely15.IsDay[i] == 1,
			Code:        resp.Minutely15.WeatherCode[i],
		}
	}

	// Pick the first slot at or after now.
	now := time.Now().In(s.loc)
	for _, slot := range slots {
		if t, err := time.ParseInLocation("2006-01-02T15:04", slot.Time, s.loc); err == nil && !t.Before(now) {
			return slot, nil
		}
	}
	return slots[len(slots)-1], nil
}

// fetchHourly fetches hourly data from 4 hours ago to 4 days ahead.
func (s *Source) fetchHourly() ([]Hour, error) {
	var resp struct {
		Hourly struct {
			Time        []string  `json:"time"`
			Temp        []float64 `json:"temperature_2m"`
			FeelsLike   []float64 `json:"apparent_temperature"`
			IsDay       []int     `json:"is_day"`
			WeatherCode []int     `json:"weather_code"`
		} `json:"hourly"`
	}

	now := time.Now().In(s.loc)
	query := url.Values{
		"models":     {"meteofrance_seamless"},
		"hourly":     {"temperature_2m,apparent_temperature,is_day,weather_code"},
		"start_date": {now.Add(-4 * time.Hour).Format(time.DateOnly)},
		"end_date":   {now.AddDate(0, 0, 4).Format(time.DateOnly)},
		"latitude":   {s.lat},
		"longitude":  {s.lon},
		"timezone":   {s.tz},
	}
	if _, err := httpx.GetJSON(apiURL, query, nil, nil, &resp, checkErrOpenMeteo); err != nil {
		return nil, err
	}

	hours := make([]Hour, len(resp.Hourly.Time))
	for i, t := range resp.Hourly.Time {
		hours[i] = Hour{
			Time:        t,
			Temperature: resp.Hourly.Temp[i],
			FeelsLike:   resp.Hourly.FeelsLike[i],
			IsDay:       resp.Hourly.IsDay[i] == 1,
			Code:        resp.Hourly.WeatherCode[i],
		}
	}
	return hours, nil
}

// fetchDaily fetches daily data from 4 to 7 days ahead.
func (s *Source) fetchDaily() ([]Day, error) {
	var resp struct {
		Daily struct {
			Time        []string  `json:"time"`
			WeatherCode []int     `json:"weather_code"`
			TempMax     []float64 `json:"temperature_2m_max"`
			TempMin     []float64 `json:"temperature_2m_min"`
		} `json:"daily"`
	}

	now := time.Now().In(s.loc)
	query := url.Values{
		"daily":      {"weather_code,temperature_2m_max,temperature_2m_min"},
		"start_date": {now.AddDate(0, 0, 4).Format(time.DateOnly)},
		"end_date":   {now.AddDate(0, 0, 7).Format(time.DateOnly)},
		"latitude":   {s.lat},
		"longitude":  {s.lon},
		"timezone":   {s.tz},
	}
	if _, err := httpx.GetJSON(apiURL, query, nil, nil, &resp, checkErrOpenMeteo); err != nil {
		return nil, err
	}

	days := make([]Day, len(resp.Daily.Time))
	for i, t := range resp.Daily.Time {
		days[i] = Day{
			Date:    t,
			TempMax: resp.Daily.TempMax[i],
			TempMin: resp.Daily.TempMin[i],
			Code:    resp.Daily.WeatherCode[i],
		}
	}
	return days, nil
}

// filterHourly keeps hours from 4 hours ago to 4 days ahead.
func filterHourly(hours []Hour, loc *time.Location) []Hour {
	now := time.Now().In(loc)
	start := now.Add(-4 * time.Hour)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 4)

	result := make([]Hour, 0, len(hours))
	for _, h := range hours {
		t, err := time.ParseInLocation("2006-01-02T15:04", h.Time, loc)
		if err != nil || t.Before(start) || t.After(end) {
			continue
		}
		result = append(result, h)
	}
	return result
}

// filterDaily keeps days from 4 to 7 days ahead.
func filterDaily(days []Day, loc *time.Location) []Day {
	now := time.Now().In(loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 4)
	end := start.AddDate(0, 0, 3)

	result := make([]Day, 0, 4)
	for _, d := range days {
		t, err := time.ParseInLocation(time.DateOnly, d.Date, loc)
		if err != nil || t.Before(start) || t.After(end) {
			continue
		}
		result = append(result, d)
	}
	return result
}

// checkErrOpenMeteo returns an error if the Open-Meteo response reports one.
func checkErrOpenMeteo(body []byte) error {
	var resp struct {
		Error  bool   `json:"error"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && resp.Error {
		return fmt.Errorf("%s", resp.Reason)
	}
	return nil
}
