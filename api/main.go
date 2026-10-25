// Command api runs the StrasBoard HTTP API.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"strasboard/api/config"
	"strasboard/api/jobs"
	"strasboard/api/server"
	"strasboard/api/source"
	"strasboard/api/source/electricity"
	"strasboard/api/source/tempo"
	"strasboard/api/source/transport"
	"strasboard/api/source/weather"
	"strasboard/api/store"
)

var version = "dev"

func main() {
	log.Printf("StrasBoard API %s", version)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	port := 8080
	if p := os.Getenv("PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}

	st := store.New()
	if err := st.Load(cfg.Snapshot.Path); err != nil {
		log.Printf("load snapshot: %v", err)
	} else {
		log.Printf("loaded snapshot from %s", cfg.Snapshot.Path)
	}

	tr := transport.New(cfg)
	resources := []source.Resource{
		weather.New(cfg),
		tr,
		electricity.New(cfg),
		tempo.New(cfg),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	schedule := jobs.NewSchedule()
	scheduler := jobs.New(st, schedule, resources...)
	go scheduler.Run(ctx)

	go snapshotLoop(ctx, st, cfg)

	srv := server.New(cfg, st, schedule, resources...)
	httpServer := &http.Server{
		Addr:    ":" + strconv.Itoa(port),
		Handler: srv.Handler(),
	}

	go func() {
		log.Printf("listening on :%d", port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	if err := st.Save(cfg.Snapshot.Path); err != nil {
		log.Printf("save snapshot: %v", err)
	}
}

// snapshotLoop periodically persists the store until ctx is cancelled.
func snapshotLoop(ctx context.Context, st *store.Store, cfg *config.Config) {
	ticker := time.NewTicker(cfg.SnapshotInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := st.Save(cfg.Snapshot.Path); err != nil {
				log.Printf("save snapshot: %v", err)
			}
		}
	}
}
