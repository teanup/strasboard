package jobs

import (
	"sync"
	"time"
)

// Schedule tracks when each resource next needs refreshing. The HTTP layer uses
// it to derive Cache-Control max-age values, so clients know how long the data
// they just fetched stays fresh.
//
// It is safe for concurrent use.
type Schedule struct {
	mu  sync.RWMutex
	fns map[string]func(time.Time) time.Time
}

// NewSchedule returns an empty schedule.
func NewSchedule() *Schedule {
	return &Schedule{fns: make(map[string]func(time.Time) time.Time)}
}

// Register records the next-refresh function for a resource.
func (s *Schedule) Register(name string, fn func(time.Time) time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fns[name] = fn
}

// MaxAge returns how long data for a resource stays fresh, measured from now.
// It returns 0 when the resource is unknown or already due for a refresh.
func (s *Schedule) MaxAge(name string, now time.Time) time.Duration {
	s.mu.RLock()
	fn, ok := s.fns[name]
	s.mu.RUnlock()
	if !ok {
		return 0
	}

	d := fn(now).Sub(now)
	if d < 0 {
		return 0
	}
	return d
}
