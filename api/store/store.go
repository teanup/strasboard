// Package store provides the single source of truth for the API.
package store

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Item is a single stored value together with its metadata.
type Item struct {
	ID        string    `json:"id"`
	Data      any       `json:"data"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Resource is a named collection of items plus collection-level metadata.
type Resource struct {
	UpdatedAt time.Time       `json:"updated_at"`
	Error     string          `json:"error,omitempty"`
	Items     map[string]Item `json:"items"`
}

// Store holds named resources. It is safe for concurrent use.
type Store struct {
	mu        sync.RWMutex
	resources map[string]*Resource
}

// New returns an empty store.
func New() *Store {
	return &Store{resources: make(map[string]*Resource)}
}

// Ensure creates a resource if it does not exist yet, so that reads of a
// not-yet-populated resource return an empty collection rather than 404.
func (s *Store) Ensure(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resource(name)
}

// resource returns the resource for name, creating it if needed.
// The caller must hold the write lock.
func (s *Store) resource(name string) *Resource {
	r, ok := s.resources[name]
	if !ok {
		r = &Resource{Items: make(map[string]Item)}
		s.resources[name] = r
	}
	return r
}

// Get returns a single item and whether it exists.
func (s *Store) Get(resource, id string) (Item, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.resources[resource]
	if !ok {
		return Item{}, false
	}
	item, ok := r.Items[id]
	return item, ok
}

// List returns all items of a resource sorted by ID, plus the resource
// metadata. The boolean reports whether the resource exists.
func (s *Store) List(resource string) ([]Item, Resource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.resources[resource]
	if !ok {
		return nil, Resource{}, false
	}

	items := make([]Item, 0, len(r.Items))
	for _, item := range r.Items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })

	meta := Resource{UpdatedAt: r.UpdatedAt, Error: r.Error}
	return items, meta, true
}

// Meta returns a resource's collection-level metadata without copying its
// items. The boolean reports whether the resource exists.
func (s *Store) Meta(resource string) (Resource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	r, ok := s.resources[resource]
	if !ok {
		return Resource{}, false
	}
	return Resource{UpdatedAt: r.UpdatedAt, Error: r.Error}, true
}

// Set stores a single item, clearing any previous resource error.
func (s *Store) Set(resource, id string, data any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r := s.resource(resource)
	r.Items[id] = Item{ID: id, Data: data, UpdatedAt: time.Now().UTC()}
	r.UpdatedAt = time.Now().UTC()
	r.Error = ""
}

// Prune keeps only the keep most recent items of a resource, ordered by ID.
// It is used by resources whose items are timestamp-keyed and bounded in
// number (e.g. the last few sensor readings). It reports how many items were
// removed.
func (s *Store) Prune(resource string, keep int) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.resources[resource]
	if !ok || len(r.Items) <= keep {
		return 0
	}

	ids := make([]string, 0, len(r.Items))
	for id := range r.Items {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	removed := 0
	for _, id := range ids[:len(ids)-keep] {
		delete(r.Items, id)
		removed++
	}
	return removed
}

// Replace swaps the entire contents of a resource in one atomic operation.
// It is used by jobs to publish a freshly fetched snapshot.
func (s *Store) Replace(resource string, items map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r := s.resource(resource)
	now := time.Now().UTC()
	r.Items = make(map[string]Item, len(items))
	for id, data := range items {
		r.Items[id] = Item{ID: id, Data: data, UpdatedAt: now}
	}
	r.UpdatedAt = now
	r.Error = ""
}

// Delete removes a single item. It reports whether the item existed.
func (s *Store) Delete(resource, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.resources[resource]
	if !ok {
		return false
	}
	if _, ok := r.Items[id]; !ok {
		return false
	}
	delete(r.Items, id)
	return true
}

// SetError marks a resource as degraded: existing items are kept but flagged
// with an error message. The resource is created if it does not exist.
func (s *Store) SetError(resource, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.resource(resource).Error = msg
}

// Resources returns the names of all known resources, sorted.
func (s *Store) Resources() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.resources))
	for name := range s.resources {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Save writes the whole store to path as JSON. The write is atomic: data is
// written to a temporary file and renamed over the target, so a crash mid-write
// cannot corrupt an existing snapshot.
func (s *Store) Save(path string) error {
	s.mu.RLock()
	snapshot := make(map[string]*Resource, len(s.resources))
	maps.Copy(snapshot, s.resources)
	s.mu.RUnlock()

	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal snapshot: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("store: create snapshot dir: %w", err)
		}
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("store: write snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("store: replace snapshot: %w", err)
	}
	return nil
}

// Load reads a snapshot from path into the store, replacing its contents.
// A missing file is not an error: the store is simply left empty.
func (s *Store) Load(path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("store: read snapshot: %w", err)
	}

	var snapshot map[string]*Resource
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return fmt.Errorf("store: decode snapshot: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources = snapshot
	for _, r := range s.resources {
		if r.Items == nil {
			r.Items = make(map[string]Item)
		}
	}
	return nil
}
