package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"
)

const (
	weatherCurrentTTL  = 1 * time.Hour
	weatherHourlyTTL   = 3 * time.Hour
	weatherDailyTTL    = 6 * time.Hour
	weatherResponseTTL = 15 * time.Minute
)

type WeatherSource struct {
	apiURL string
	lat    string
	lon    string
	tz     string
	loc    *time.Location

	mu      sync.Mutex
	current *weatherCache[[]WeatherCurrent]
	hourly  *weatherCache[[]WeatherHour]
	daily   *weatherCache[[]WeatherDay]
}

type weatherCache[T any] struct {
	data      T
	expiresAt time.Time
}

func (c *weatherCache[T]) valid() bool {
	return c != nil && time.Now().Before(c.expiresAt)
}

// API response
type WeatherData struct {
	Current WeatherCurrent `json:"current"`
	Hourly  []WeatherHour  `json:"hourly"`
	Daily   []WeatherDay   `json:"daily"`
}

type WeatherCurrent struct {
	Time        string  `json:"time"`
	Temperature float64 `json:"temperature"`
	FeelsLike   float64 `json:"feels_like"`
	IsDay       bool    `json:"is_day"`
	Code        int     `json:"code"`
}

type WeatherHour struct {
	Time        string  `json:"time"`
	Temperature float64 `json:"temperature"`
	FeelsLike   float64 `json:"feels_like"`
	IsDay       bool    `json:"is_day"`
	Code        int     `json:"code"`
}

type WeatherDay struct {
	Date    string  `json:"date"`
	TempMax float64 `json:"temp_max"`
	TempMin float64 `json:"temp_min"`
	Code    int     `json:"code"`
}

func NewWeatherSource(cfg *Config) *WeatherSource {
	loc, _ := time.LoadLocation(cfg.WeatherTimezone)
	if loc == nil {
		loc = time.Local
	}
	return &WeatherSource{
		apiURL: cfg.WeatherAPIURL,
		lat:    fmt.Sprintf("%.4f", cfg.WeatherLatitude),
		lon:    fmt.Sprintf("%.4f", cfg.WeatherLongitude),
		tz:     cfg.WeatherTimezone,
		loc:    loc,
	}
}

func (s *WeatherSource) Name() string               { return "weather" }
func (s *WeatherSource) DegradedTTL() time.Duration { return 24 * time.Hour }

func (s *WeatherSource) Fetch() *Response {
	if s.apiURL == "" {
		return ErrorResponse("weather not configured", time.Hour)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var lastErr error
	if !s.current.valid() {
		if err := s.fetchCurrent(); err != nil {
			log.Printf("[weather] fetch current: %v", err)
			lastErr = err
		}
	}
	if !s.hourly.valid() {
		if err := s.fetchHourly(); err != nil {
			log.Printf("[weather] fetch hourly: %v", err)
			lastErr = err
		}
	}
	if !s.daily.valid() {
		if err := s.fetchDaily(); err != nil {
			log.Printf("[weather] fetch daily: %v", err)
			lastErr = err
		}
	}

	if !s.current.valid() && !s.hourly.valid() && !s.daily.valid() {
		return ErrorResponse("weather unavailable: "+lastErr.Error(), 5*time.Minute)
	}

	data := WeatherData{}
	if s.current.valid() {
		data.Current = s.filterCurrent()
	}
	if s.hourly.valid() {
		data.Hourly = s.filterHourly()
	}
	if s.daily.valid() {
		data.Daily = s.filterDaily()
	}

	// Refresh filtered data at midnight
	now := time.Now().In(s.loc)
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, s.loc)
	if midnight.Sub(now) < weatherResponseTTL {
		return NewResponseUntil(data, midnight)
	}
	return NewResponse(data, weatherResponseTTL)
}

// Fetch 15-minutely weather data
func (s *WeatherSource) fetchCurrent() error {
	var resp struct {
		Minutely15 struct {
			Time        []string  `json:"time"`
			Temp        []float64 `json:"temperature_2m"`
			FeelsLike   []float64 `json:"apparent_temperature"`
			IsDay       []int     `json:"is_day"`
			WeatherCode []int     `json:"weather_code"`
		} `json:"minutely_15"`
	}

	// Fetch for next 2 hours
	query := url.Values{
		"models":               {"meteofrance_seamless"},
		"minutely_15":          {"temperature_2m,apparent_temperature,is_day,weather_code"},
		"forecast_minutely_15": {"8"},
		"latitude":             {s.lat},
		"longitude":            {s.lon},
		"timezone":             {s.tz},
	}
	if _, err := GetJSON(s.apiURL, query, nil, nil, &resp, checkErrOpenMeteo); err != nil {
		return err
	}
	if len(resp.Minutely15.Time) == 0 {
		return fmt.Errorf("no data")
	}

	slots := make([]WeatherCurrent, len(resp.Minutely15.Time))
	for i, t := range resp.Minutely15.Time {
		slots[i] = WeatherCurrent{
			Time:        t,
			Temperature: resp.Minutely15.Temp[i],
			FeelsLike:   resp.Minutely15.FeelsLike[i],
			IsDay:       resp.Minutely15.IsDay[i] == 1,
			Code:        resp.Minutely15.WeatherCode[i],
		}
	}

	s.current = &weatherCache[[]WeatherCurrent]{data: slots, expiresAt: time.Now().Add(weatherCurrentTTL)}
	return nil
}

// Fetch hourly weather data
func (s *WeatherSource) fetchHourly() error {

	var resp struct {
		Hourly struct {
			Time        []string  `json:"time"`
			Temp        []float64 `json:"temperature_2m"`
			FeelsLike   []float64 `json:"apparent_temperature"`
			IsDay       []int     `json:"is_day"`
			WeatherCode []int     `json:"weather_code"`
		} `json:"hourly"`
	}

	// Fetch from hour-4 to day+3+TTL
	now := time.Now().In(s.loc)
	startDate := now.Add(-4 * time.Hour).Format(time.DateOnly)
	endDate := now.AddDate(0, 0, 3).Add(weatherHourlyTTL + weatherResponseTTL).Format(time.DateOnly)
	query := url.Values{
		"models":     {"meteofrance_seamless"},
		"hourly":     {"temperature_2m,apparent_temperature,is_day,weather_code"},
		"start_date": {startDate},
		"end_date":   {endDate},
		"latitude":   {s.lat},
		"longitude":  {s.lon},
		"timezone":   {s.tz},
	}
	if _, err := GetJSON(s.apiURL, query, nil, nil, &resp, checkErrOpenMeteo); err != nil {
		return err
	}

	hours := make([]WeatherHour, len(resp.Hourly.Time))
	for i, t := range resp.Hourly.Time {
		hours[i] = WeatherHour{
			Time:        t,
			Temperature: resp.Hourly.Temp[i],
			FeelsLike:   resp.Hourly.FeelsLike[i],
			IsDay:       resp.Hourly.IsDay[i] == 1,
			Code:        resp.Hourly.WeatherCode[i],
		}
	}

	s.hourly = &weatherCache[[]WeatherHour]{data: hours, expiresAt: time.Now().Add(weatherHourlyTTL)}
	return nil
}

// Fetch daily weather data
func (s *WeatherSource) fetchDaily() error {
	var resp struct {
		Daily struct {
			Time        []string  `json:"time"`
			WeatherCode []int     `json:"weather_code"`
			TempMax     []float64 `json:"temperature_2m_max"`
			TempMin     []float64 `json:"temperature_2m_min"`
		} `json:"daily"`
	}

	// Fetch from day+4 to day+7+TTL
	now := time.Now().In(s.loc)
	startDate := now.AddDate(0, 0, 4).Format(time.DateOnly)
	endDate := now.AddDate(0, 0, 7).Add(weatherDailyTTL + weatherResponseTTL).Format(time.DateOnly)
	query := url.Values{
		"daily":      {"weather_code,temperature_2m_max,temperature_2m_min"},
		"start_date": {startDate},
		"end_date":   {endDate},
		"latitude":   {s.lat},
		"longitude":  {s.lon},
		"timezone":   {s.tz},
	}
	if _, err := GetJSON(s.apiURL, query, nil, nil, &resp, checkErrOpenMeteo); err != nil {
		return err
	}

	days := make([]WeatherDay, len(resp.Daily.Time))
	for i, t := range resp.Daily.Time {
		days[i] = WeatherDay{
			Date:    t,
			TempMax: resp.Daily.TempMax[i],
			TempMin: resp.Daily.TempMin[i],
			Code:    resp.Daily.WeatherCode[i],
		}
	}

	s.daily = &weatherCache[[]WeatherDay]{data: days, expiresAt: time.Now().Add(weatherDailyTTL)}
	return nil
}

// Filter current weather
func (s *WeatherSource) filterCurrent() WeatherCurrent {
	// Find closest slot to midpoint of TTL interval
	now := time.Now().In(s.loc)
	midpoint := now.Add((weatherResponseTTL - 15*time.Minute) / 2)

	for _, slot := range s.current.data {
		if t, err := time.ParseInLocation("2006-01-02T15:04", slot.Time, s.loc); err == nil {
			if t.After(midpoint) {
				return slot
			}
		}
	}
	return s.current.data[len(s.current.data)-1]
}

// Filter hourly data
func (s *WeatherSource) filterHourly() []WeatherHour {
	// Filter from hour-4 to day+4 at 00:00
	now := time.Now().In(s.loc)
	start := now.Add(-4 * time.Hour)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, 4)

	result := make([]WeatherHour, 0, 100)
	for _, h := range s.hourly.data {
		t, err := time.ParseInLocation("2006-01-02T15:04", h.Time, s.loc)
		if err != nil || t.Before(start) || t.After(end) {
			continue
		}
		result = append(result, h)
	}
	return result
}

// Filter daily data
func (s *WeatherSource) filterDaily() []WeatherDay {
	// Filter from day+4 to day+7
	now := time.Now().In(s.loc)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc).AddDate(0, 0, 4)
	end := start.AddDate(0, 0, 3)

	result := make([]WeatherDay, 0, 4)
	for _, d := range s.daily.data {
		t, err := time.ParseInLocation(time.DateOnly, d.Date, s.loc)
		if err != nil || t.Before(start) || t.After(end) {
			continue
		}
		result = append(result, d)
	}
	return result
}

// Check for error in Open-Meteo response
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
