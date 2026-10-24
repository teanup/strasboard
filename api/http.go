package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

var (
	httpClient     = &http.Client{Timeout: 10 * time.Second}
	httpNoRedirect = &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
)

// Perform a GET request and decode the JSON response
func GetJSON(baseURL string, query url.Values, headers http.Header, cookies []*http.Cookie, dest any, errCheck func([]byte) error) (*http.Response, error) {
	return request("GET", buildURL(baseURL, query), nil, "", headers, cookies, true, dest, errCheck)
}

// Perform a POST request with JSON payload and decode the response
func PostJSON(reqURL string, payload any, headers http.Header, cookies []*http.Cookie, dest any, errCheck func([]byte) error) (*http.Response, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to encode payload: %w", err)
		}
	}
	return request("POST", reqURL, body, "application/json", headers, cookies, true, dest, errCheck)
}

// Perform a POST request with form data and decode the response
func PostForm(reqURL string, params url.Values, headers http.Header, cookies []*http.Cookie, dest any, errCheck func([]byte) error) (*http.Response, error) {
	return request("POST", reqURL, []byte(params.Encode()), "application/x-www-form-urlencoded", headers, cookies, true, dest, errCheck)
}

// Perform a GET request without following redirects
func GetRedirect(baseURL string, query url.Values, headers http.Header, cookies []*http.Cookie) (*http.Response, error) {
	return request("GET", buildURL(baseURL, query), nil, "", headers, cookies, false, nil, nil)
}

// Generic HTTP request function
func request(method, reqURL string, body []byte, contentType string, headers http.Header, cookies []*http.Cookie, follow bool, dest any, errCheck func([]byte) error) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header[k] = v
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}

	client := httpClient
	if !follow {
		client = httpNoRedirect
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if !follow && resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp, nil
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return resp, fmt.Errorf("server returned %d: %s", resp.StatusCode, truncate(data, 100))
	}

	if errCheck != nil {
		if err := errCheck(data); err != nil {
			return resp, err
		}
	}

	if dest != nil {
		if err := json.Unmarshal(data, dest); err != nil {
			return resp, fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return resp, nil
}

// Build URL with query parameters
func buildURL(base string, query url.Values) string {
	if len(query) == 0 {
		return base
	}
	return base + "?" + query.Encode()
}

// Truncate byte slice
func truncate(b []byte, maxLen int) string {
	if len(b) <= maxLen {
		return string(b)
	}
	return string(b[:maxLen]) + "..."
}
