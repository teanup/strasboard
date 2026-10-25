// Package jobs runs background refreshers that pull data from external
// sources into the store.
package jobs

import (
	"context"
	"log"
	"sync"
	"time"

	"strasboard/api/source"
	"strasboard/api/store"
)

// Scheduler runs a set of resources, each on its own schedule.
type Scheduler struct {
	store     *store.Store
	resources []source.Resource
}

// New returns a Scheduler that publishes refreshed data into st.
func New(st *store.Store, resources ...source.Resource) *Scheduler {
	return &Scheduler{store: st, resources: resources}
}

// Run starts every resource's refresh loop and blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, r := range s.resources {
		wg.Add(1)
		go func(r source.Resource) {
			defer wg.Done()
			s.run(ctx, r)
		}(r)
	}
	wg.Wait()
}

// run refreshes a single resource, sleeping until each next refresh time.
func (s *Scheduler) run(ctx context.Context, r source.Resource) {
	for {
		s.refresh(ctx, r)

		wait := max(time.Until(r.NextRefresh(time.Now())), 0)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// refresh performs one refresh cycle and publishes the result to the store.
func (s *Scheduler) refresh(ctx context.Context, r source.Resource) {
	items, err := r.Refresh(ctx)
	if err != nil {
		log.Printf("[%s] refresh failed: %v", r.Name(), err)
		s.store.SetError(r.Name(), err.Error())
		return
	}
	s.store.Replace(r.Name(), items)
	log.Printf("[%s] refreshed %d item(s)", r.Name(), len(items))
}
