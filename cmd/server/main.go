// Command server runs the EntregaViva HTTP server.
//
// Usage:
//
//	server              start the server (configured through environment variables)
//	server healthcheck  probe GET /api/health on the local port; exit 0 when healthy
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/codebyjuliamendes/entregaviva"
	"github.com/codebyjuliamendes/entregaviva/internal/config"
	"github.com/codebyjuliamendes/entregaviva/internal/events"
	"github.com/codebyjuliamendes/entregaviva/internal/httpapi"
	"github.com/codebyjuliamendes/entregaviva/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() { os.Exit(realMain(os.Args[1:])) }

func realMain(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "healthcheck":
			return healthcheck()
		case "version", "-version", "--version":
			fmt.Println(version)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (use: healthcheck, version)\n", args[0])
			return 2
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("server stopped with error", "err", err)
		return 1
	}
	return 0
}

func run(ctx context.Context) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	st, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Error("close database", "err", err)
		}
	}()
	if cfg.SeedDemo {
		seeded, err := st.SeedDemo(ctx, time.Now())
		if err != nil {
			return fmt.Errorf("seed demo data: %w", err)
		}
		if seeded {
			log.Info("demo orders created")
		}
	}

	hub := events.NewHub(events.Options{MaxSubscribers: cfg.SSEMaxClients})
	handler, err := httpapi.New(httpapi.Deps{Store: st, Hub: hub, Log: log, Config: cfg, Static: entregaviva.Public()})
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Addr(),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second, // SSE handlers extend their own deadline per write
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	// SSE handlers never finish on their own; closing the hub ends them so
	// Shutdown can drain.
	srv.RegisterOnShutdown(hub.Close)

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		return err
	}
	log.Info("listening", "addr", ln.Addr().String(), "version", version,
		"database", cfg.DatabasePath, "operator_auth", cfg.OperatorPassword != "")

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}
	log.Info("shutting down", "timeout", cfg.ShutdownTimeout.String())
	sctx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped")
	return nil
}

// healthcheck lets distroless images (no shell, no curl) define HEALTHCHECK.
func healthcheck() int {
	port := os.Getenv("PORT")
	if _, err := strconv.Atoi(port); err != nil {
		port = "3003"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/api/health", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}
