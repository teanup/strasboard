package main

import (
	"sync"
	"time"
)

type Cache struct {
	mu    sync.RWMutex
	items map[string]cacheItem
}

type cacheItem struct {
	data   *Response
	backup *Response
}

func NewCache() *Cache {
	return &Cache{items: make(map[string]cacheItem)}
}

// Get cached response if valid
func (c *Cache) Get(key string) *Response {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, ok := c.items[key]
	if !ok || item.data == nil || time.Now().After(item.data.ExpiresAt) {
		return nil
	}
	return item.data
}

// Get backup response for degraded mode
func (c *Cache) GetBackup(key string) *Response {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, ok := c.items[key]
	if !ok || item.backup == nil || time.Now().After(item.backup.ExpiresAt) {
		return nil
	}
	return item.backup
}

// Store response and update backup if successful
func (c *Cache) Set(key string, resp *Response, degradedTTL time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	item := c.items[key]
	item.data = resp

	if resp.Error == "" {
		item.backup = BackupResponse(resp, degradedTTL)
	}

	c.items[key] = item
}
