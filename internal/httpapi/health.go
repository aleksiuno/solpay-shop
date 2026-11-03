package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Pinger checks that the database answers. *pgxpool.Pool is one.
type Pinger interface {
	Ping(ctx context.Context) error
}

// pingTimeout bounds the database check, so a database that hangs fails the check instead of
// hanging it.
const pingTimeout = 2 * time.Second

// health answers 200 "ok" when the database answers within pingTimeout, and 503 otherwise. It
// answers in plain text, for curl and probes, and logs the database's error instead of sending it.
func health(db Pinger, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := db.Ping(ctx); err != nil {
			log.LogAttrs(r.Context(), slog.LevelWarn, "health check failed: the database does not answer",
				slog.Any("error", err))
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "database unavailable\n")
			return
		}
		_, _ = io.WriteString(w, "ok\n")
	})
}
