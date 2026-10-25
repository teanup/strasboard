// Package client posts sensor readings to the StrasBoard API.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// Reading is a single temperature/humidity measurement.
type Reading struct {
	Temperature float64 `json:"temperature"`
	Humidity    float64 `json:"humidity"`
	Location    string  `json:"location"`
}

// Client posts readings to a StrasBoard API endpoint.
type Client struct {
	url       string
	authToken string
	http      *http.Client
}

// New returns a Client posting to url, optionally authenticating with a token.
func New(url, authToken string) *Client {
	return &Client{
		url:       url,
		authToken: authToken,
		http:      &http.Client{Timeout: 10 * time.Second},
	}
}

// Post sends a reading to the API.
func (c *Client) Post(r Reading) error {
	payload := map[string]any{
		"id":   strconv.FormatInt(time.Now().UnixMilli(), 10),
		"data": r,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", c.authToken)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server returned %d", resp.StatusCode)
	}
	return nil
}
