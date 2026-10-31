// Command server runs the solpay-shop HTTP service.
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
	"path/filepath"
	"syscall"
	"time"

	"github.com/aleksiuno/solpay-shop/internal/config"
)

// shutdownTimeout stays below Docker's default 10 s stop grace period, so a shutdown that times
// out is still logged before the container is killed.
const shutdownTimeout = 8 * time.Second

func main() {
	stdout := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		stdout.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	logger, closeLog, err := newLogger(stdout, cfg.LogFile)
	if err != nil {
		stdout.Error("open log file", "error", err)
		os.Exit(1)
	}

	runErr := run(logger, cfg)
	if runErr != nil {
		// Logged before the file is closed, so LOG_FILE also records why the server stopped.
		logger.Error("server exited with an error", "error", runErr)
	}
	if err := closeLog(); err != nil {
		stdout.Error("close log file", "error", err)
	}
	if runErr != nil {
		os.Exit(1)
	}
}

func run(logger *slog.Logger, cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	logger.InfoContext(ctx, "server started", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	stop() // a second Ctrl-C now kills the process instead of waiting for the shutdown

	logger.InfoContext(ctx, "shutting down", "cause", context.Cause(ctx))
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.InfoContext(ctx, "server stopped")
	return nil
}

// newLogger returns a JSON logger that writes to stdout and, when path is set, also appends to
// that file. The file is kept at mode 0600, even if it already existed, and a new directory gets
// 0700, because log lines will carry order ids, which act as bearer tokens.
func newLogger(stdout *slog.Logger, path string) (*slog.Logger, func() error, error) {
	if path == "" {
		return stdout, func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, nil, errors.Join(err, f.Close())
	}
	h := slog.NewMultiHandler(stdout.Handler(), slog.NewJSONHandler(f, nil))
	return slog.New(h), f.Close, nil
}
