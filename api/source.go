package main

import "time"

type Source interface {
	Name() string
	Fetch() *Response
	DegradedTTL() time.Duration
}

type Response struct {
	Data      any       `json:"data,omitempty"`
	Timestamp string    `json:"timestamp"`
	Error     string    `json:"error,omitempty"`
	ExpiresAt time.Time `json:"-"`
}

// Create a successful response with TTL
func NewResponse(data any, ttl time.Duration) *Response {
	return &Response{
		Data:      data,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: time.Now().Add(ttl),
	}
}

// Create a successful response with explicit expiration time
func NewResponseUntil(data any, expiresAt time.Time) *Response {
	return &Response{
		Data:      data,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: expiresAt,
	}
}

// Create an error response
func ErrorResponse(msg string, ttl time.Duration) *Response {
	return &Response{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Error:     msg,
		ExpiresAt: time.Now().Add(ttl),
	}
}

// Create a backup response from a successful response with degraded TTL
func BackupResponse(original *Response, degradedTTL time.Duration) *Response {
	return &Response{
		Data:      original.Data,
		Timestamp: original.Timestamp,
		ExpiresAt: time.Now().Add(degradedTTL),
	}
}

// Create a degraded response combining backup data with error info
func DegradedResponse(backup *Response, err *Response) *Response {
	return &Response{
		Data:      backup.Data,
		Timestamp: backup.Timestamp,
		Error:     err.Error,
		ExpiresAt: err.ExpiresAt,
	}
}
