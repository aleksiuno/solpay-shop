// Command server runs the solpay-shop HTTP service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aleksiuno/solpay-shop/internal/audit"
	"github.com/aleksiuno/solpay-shop/internal/config"
	"github.com/aleksiuno/solpay-shop/internal/db"
	"github.com/aleksiuno/solpay-shop/internal/download"
	"github.com/aleksiuno/solpay-shop/internal/httpapi"
	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/payment"
	"github.com/aleksiuno/solpay-shop/internal/product"
	"github.com/aleksiuno/solpay-shop/internal/solana"
	"github.com/aleksiuno/solpay-shop/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// shutdownTimeout bounds the HTTP server's shutdown. With up to 2 s for the payment watcher to stop
// after it, the whole shutdown stays below Docker's default 10 s stop grace period, so one that
// times out is still logged before the container is killed.
const shutdownTimeout = 7 * time.Second

func main() {
	migrateOnly := flag.Bool("migrate-only", false, "apply pending database migrations and exit")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "unexpected arguments: %v\n", flag.Args())
		flag.Usage()
		os.Exit(2)
	}

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
	// Lines logged with a request's context carry its request id.
	logger = slog.New(httpapi.ContextHandler{Handler: logger.Handler()})

	runErr := run(logger, cfg, *migrateOnly)
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

func run(logger *slog.Logger, cfg config.Config, migrateOnly bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal, restore the default handlers, so a second Ctrl-C kills the process in
	// any phase, including a migration that is still connecting or waiting for its lock.
	context.AfterFunc(ctx, stop)

	logger.InfoContext(ctx, "starting", "version", version, "migrate_only", migrateOnly)
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		return err
	}
	logger.InfoContext(ctx, "database schema is up to date")
	if migrateOnly {
		return nil
	}
	if ctx.Err() != nil {
		logger.InfoContext(ctx, "stopping before the server starts", "cause", context.Cause(ctx))
		return nil
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		if ctx.Err() != nil {
			logger.InfoContext(ctx, "stopping before the server starts", "cause", context.Cause(ctx))
			return nil
		}
		return err
	}
	// Deferred, so the pool closes only after the server has shut down and no request can use it.
	defer pool.Close()

	files, err := download.OpenFiles(cfg.FilesDir)
	if err != nil {
		return err
	}
	defer files.Close()
	products := product.NewService(db.NewProducts(pool), files)
	eventLog := db.NewEvents(pool)
	events := audit.NewRecorder(eventLog, logger)
	orders := order.NewService(db.NewOrders(pool, events), products, cfg.OrderTTL, solana.NewReference, now)
	downloads := download.NewService(db.NewDownloads(pool, events), files, cfg.DownloadSecret, now)
	chain, err := solana.New(cfg.SolanaRPCURL)
	if err != nil {
		return err
	}
	watchCfg := payment.DefaultConfig(cfg.PollInterval)
	payments := db.NewPayments(pool, events)
	watcher := payment.NewWatcher(chain, payments, events, cfg.MerchantAddress, watchCfg, now)

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.Addr)
	if err != nil {
		return err
	}

	// The watcher has a context of its own, so that it keeps running while the server shuts down
	// and stops only after it, before the pool closes. stopWatcher stops it and returns the error
	// it stopped with; every return below calls it.
	watchCtx, cancelWatcher := context.WithCancel(context.WithoutCancel(ctx))
	var watchErr error
	watchDone := make(chan struct{}) // closed once watchErr is set
	go func() {
		defer close(watchDone)
		watchErr = watcher.Run(watchCtx)
	}()
	stopWatcher := func() error {
		cancelWatcher()
		<-watchDone
		if watchErr != nil {
			return fmt.Errorf("payment watcher: %w", watchErr)
		}
		return nil
	}

	// The requests' contexts, which a shutdown that times out cancels. pool.Close waits for every
	// connection to come back, so it would otherwise wait for any request still running.
	reqCtx, cancelRequests := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRequests()

	site := web.Deps{
		Products: products, Orders: orders, Downloads: downloads, Merchant: cfg.MerchantAddress, Now: now,
		ExpiryGrace: watchCfg.ExpiryGrace, ProductAdmin: products, ProductFiles: files, OrderAdmin: orders,
		Ledger: payments, EventLog: eventLog, Log: logger,
	}
	api := httpapi.Deps{
		DB: pool, Products: products, Orders: orders, Downloads: downloads, ProductAdmin: products,
		Merchant: cfg.MerchantAddress, Now: now, Log: logger,
	}
	srv := &http.Server{
		Handler:           routes(cfg, logger, site, api),
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	logger.InfoContext(ctx, "server started", "addr", ln.Addr().String())

	// The watcher returns early only when it cannot work, such as on a node that is not devnet.
	// The server then shuts down as on a signal, and exits with the watcher's error.
	select {
	case err := <-serveErr:
		return errors.Join(fmt.Errorf("serve: %w", err), stopWatcher())
	case <-watchDone:
		logger.InfoContext(ctx, "shutting down", "cause", stopWatcher())
	case <-ctx.Done():
		logger.InfoContext(ctx, "shutting down", "cause", context.Cause(ctx))
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		// Ends the requests still running and their queries, then their connections.
		cancelRequests()
		return errors.Join(fmt.Errorf("shutdown: %w", err), srv.Close(), stopWatcher())
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return errors.Join(fmt.Errorf("serve: %w", err), stopWatcher())
	}
	err = stopWatcher()
	logger.InfoContext(ctx, "server stopped")
	return err
}

// now returns the time in UTC, truncated to the microseconds that Postgres stores, so the times an
// order is created with are the ones a later read gives back.
func now() time.Time {
	return utcMicros(time.Now())
}

func utcMicros(t time.Time) time.Time {
	return t.UTC().Truncate(time.Microsecond)
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
