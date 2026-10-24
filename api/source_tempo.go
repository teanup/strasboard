package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	tempoRetryTTL        = 1 * time.Hour
	tempoProvisionalHour = 8
	tempoDefinitiveHour  = 11
)

type TempoSource struct {
	apiURL    string
	authURL   string
	authToken string
	loc       *time.Location
}

// API response
type TempoData []TempoDay

type TempoDay struct {
	Date  string `json:"date"`
	Color string `json:"color"`
}

func NewTempoSource(cfg *Config) *TempoSource {
	loc, _ := time.LoadLocation("Europe/Paris")
	if loc == nil {
		loc = time.Local
	}
	return &TempoSource{
		apiURL:    cfg.TempoAPIURL,
		authURL:   cfg.TempoAuthURL,
		authToken: cfg.TempoAuthToken,
		loc:       loc,
	}
}

func (s *TempoSource) Name() string               { return "tempo" }
func (s *TempoSource) DegradedTTL() time.Duration { return 24 * time.Hour }

func (s *TempoSource) Fetch() *Response {
	if s.authToken == "" {
		return ErrorResponse("tempo not configured", time.Hour)
	}

	data, err := s.fetchData()
	if err != nil {
		log.Printf("[tempo] %v", err)
		return ErrorResponse(err.Error(), 10*time.Minute)
	}

	now := time.Now().In(s.loc)
	hour := now.Hour()

	// Tomorrow's data not yet available
	if hour < tempoProvisionalHour {
		expiresAt := time.Date(now.Year(), now.Month(), now.Day(), tempoProvisionalHour, 0, 0, 0, s.loc)
		return NewResponseUntil(data, expiresAt)
	}
	// Tomorrow's data should be available
	if hour >= tempoDefinitiveHour {
		expiresAt := time.Date(now.Year(), now.Month(), now.Day(), tempoProvisionalHour, 0, 0, 0, s.loc).AddDate(0, 0, 1)
		return NewResponseUntil(data, expiresAt)
	}
	// Retry until data is definitive
	return NewResponse(data, tempoRetryTTL)
}

// Fetch tempo data from RTE
func (s *TempoSource) fetchData() (TempoData, error) {
	token, err := s.authenticate()
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}

	now := time.Now().In(s.loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, s.loc)
	endDate := today.AddDate(0, 0, 2)

	var resp struct {
		TempoLikeCalendars struct {
			Values []struct {
				StartDate string `json:"start_date"`
				Value     string `json:"value"`
			} `json:"values"`
		} `json:"tempo_like_calendars"`
	}

	query := url.Values{
		"start_date": {today.Format(time.RFC3339)},
		"end_date":   {endDate.Format(time.RFC3339)},
	}
	headers := http.Header{"Authorization": {"Bearer " + token}}
	if _, err := GetJSON(s.apiURL+"/tempo_like_calendars", query, headers, nil, &resp, checkErrRTE); err != nil {
		return nil, err
	}

	if len(resp.TempoLikeCalendars.Values) == 0 {
		return nil, fmt.Errorf("no tempo data")
	}

	data := make(TempoData, 0, len(resp.TempoLikeCalendars.Values))
	for _, v := range resp.TempoLikeCalendars.Values {
		data = append(data, TempoDay{
			Date:  v.StartDate[:10],
			Color: strings.ToLower(v.Value),
		})
	}
	return data, nil
}

// Authenticate and obtain access token
func (s *TempoSource) authenticate() (string, error) {
	var resp struct {
		AccessToken string `json:"access_token"`
	}

	headers := http.Header{"Authorization": {"Basic " + s.authToken}}
	if _, err := PostJSON(s.authURL, nil, headers, nil, &resp, nil); err != nil {
		return "", err
	}

	if resp.AccessToken == "" {
		return "", fmt.Errorf("no access token")
	}
	return resp.AccessToken, nil
}

// Check for error in RTE response
func checkErrRTE(body []byte) error {
	var resp struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && resp.Error != "" {
		if resp.Description != "" {
			return fmt.Errorf("%s: %s", resp.Error, resp.Description)
		}
		return fmt.Errorf("%s", resp.Error)
	}
	return nil
}
