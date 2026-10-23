package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OliverPanda/sub2api/bridge/internal/app"
	"github.com/OliverPanda/sub2api/bridge/internal/config"
	"github.com/OliverPanda/sub2api/bridge/internal/dujiao"
	"github.com/OliverPanda/sub2api/bridge/internal/store"
	"github.com/OliverPanda/sub2api/bridge/internal/sub2api"
)

func main() {
	logger := log.New(os.Stdout, "bridge ", log.LstdFlags|log.LUTC)
	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("configuration error: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Fatalf("database error: %v", err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		logger.Fatalf("migration error: %v", err)
	}

	httpClient := &http.Client{Timeout: cfg.RequestTimeout}
	dj := dujiao.New(cfg.DujiaoBaseURL, cfg.DujiaoChannelKey, cfg.DujiaoChannelSecret, httpClient)
	s2a := sub2api.New(cfg.Sub2APIBaseURL, cfg.Sub2APIAdminKey, httpClient)
	application := app.New(cfg, st, dj, s2a, logger)

	go application.RunWorker(ctx)

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           application.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      40 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	go func() {
		logger.Printf("listening on %s", cfg.HTTPAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Printf("HTTP shutdown error: %v", err)
	}
}
