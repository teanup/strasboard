// Package tempo fetches the EDF Tempo tariff calendar from RTE.
package tempo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"strasboard/api/config"
	"strasboard/api/httpx"
)

// apiURL and authURL are the RTE Tempo endpoints.
const (
	apiURL  = "https://digital.iservices.rte-france.com/open_api/tempo_like_supply_contract/v1"
	authURL = "https://digital.iservices.rte-france.com/token/oauth/"
)

// The next day's color is published provisionally at provisionalHour and
// becomes definitive at definitiveHour.
const (
	provisionalHour = 8
	definitiveHour  = 11
)

// retryInterval is how long to wait before retrying while the next day's
// color is still provisional.
const retryInterval = 1 * time.Hour

// Source fetches the EDF Tempo calendar from RTE.
type Source struct {
	authToken string
	loc       *time.Location

	mu       sync.Mutex
	lastDays []Day
}

// Day is a single day of the Tempo calendar.
type Day struct {
	Date  string `json:"date"`
	Color string `json:"color"`
}

func New(cfg *config.Config) *Source {
	loc, _ := time.LoadLocation("Europe/Paris")
	if loc == nil {
		loc = time.Local
	}
	return &Source{
		authToken: cfg.Tempo.AuthToken,
		loc:       loc,
	}
}

func (s *Source) Name() string { return "tempo" }

// NextRefresh returns the next time the calendar can change: the provisional
// hour while tomorrow's color is still provisional, otherwise the provisional
// hour the next day.
func (s *Source) NextRefresh(now time.Time) time.Time {
	now = now.In(s.loc)

	s.mu.Lock()
	provisional := s.hasTomorrow(now)
	s.mu.Unlock()

	if provisional && now.Hour() < definitiveHour {
		return now.Add(retryInterval)
	}
	return time.Date(now.Year(), now.Month(), now.Day()+1, provisionalHour, 0, 0, 0, s.loc)
}

// hasTomorrow reports whether the stored calendar already includes tomorrow.
func (s *Source) hasTomorrow(now time.Time) bool {
	tomorrow := now.AddDate(0, 0, 1).Format(time.DateOnly)
	for _, d := range s.lastDays {
		if d.Date >= tomorrow {
			return true
		}
	}
	return false
}

// Refresh fetches the calendar and returns one item per day keyed by date.
func (s *Source) Refresh(_ context.Context) (map[string]any, error) {
	if s.authToken == "" {
		return nil, fmt.Errorf("tempo not configured")
	}

	days, err := s.fetchData()
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.lastDays = days
	s.mu.Unlock()

	items := make(map[string]any, len(days))
	for _, d := range days {
		items[d.Date] = d
	}
	return items, nil
}

// fetchData fetches the Tempo calendar from RTE.
func (s *Source) fetchData() ([]Day, error) {
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
	if _, err := httpx.GetJSON(apiURL+"/tempo_like_calendars", query, headers, nil, &resp, checkErrRTE); err != nil {
		return nil, err
	}

	if len(resp.TempoLikeCalendars.Values) == 0 {
		return nil, fmt.Errorf("no tempo data")
	}

	data := make([]Day, 0, len(resp.TempoLikeCalendars.Values))
	for _, v := range resp.TempoLikeCalendars.Values {
		data = append(data, Day{
			Date:  v.StartDate[:10],
			Color: strings.ToLower(v.Value),
		})
	}
	return data, nil
}

// authenticate authenticates and returns an access token.
func (s *Source) authenticate() (string, error) {
	var resp struct {
		AccessToken string `json:"access_token"`
	}

	headers := http.Header{"Authorization": {"Basic " + s.authToken}}
	if _, err := httpx.PostJSON(authURL, nil, headers, nil, &resp, nil); err != nil {
		return "", err
	}

	if resp.AccessToken == "" {
		return "", fmt.Errorf("no access token")
	}
	return resp.AccessToken, nil
}

// checkErrRTE returns an error if the RTE response reports one.
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
