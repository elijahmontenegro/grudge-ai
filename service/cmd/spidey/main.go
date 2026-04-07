package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/emontenegr/spidey/service/config"
	"github.com/emontenegr/spidey/service/storage"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := storage.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer db.Close()

	mux := http.NewServeMux()
	// Proxy routes, GraphQL, static assets registered here

	addr := "spidey.localhost:8420"
	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Printf("Spidey listening on http://%s", addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("server: %v", err)
	}
}
