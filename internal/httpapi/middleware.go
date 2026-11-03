// Package httpapi holds the server's HTTP middleware (request ids, the access log, panic recovery
// and basic auth) and the API's routes.
package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"runtime/debug"
	"time"
	"uuid"
)

// RequestID gives every request a new random id, a version 4 UUID. It sends the id in the
// X-Request-ID response header and puts it on the request's context, where ContextHandler finds
// it. An X-Request-ID that the client sends is ignored, so an id in the logs is always one the
// server made.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uuid.NewV4().String()
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// AccessLog returns middleware that logs one line per request, when the handler returns: the
// method, the path, the status, the bytes of body written and the time taken. The path never
// includes the query string, because a download link carries its signature there.
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			method, path := r.Method, r.URL.Path
			rec := &recorder{ResponseWriter: w}
			// Deferred, so a response that Recover aborts is logged too.
			defer func() {
				log.LogAttrs(r.Context(), slog.LevelInfo, "request",
					slog.String("method", method),
					slog.String("path", path),
					slog.Int("status", rec.status()),
					slog.Int64("bytes", rec.bytes),
					slog.Duration("duration", time.Since(start)))
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// Recover returns middleware that turns a panic in a handler into a 500 internal envelope, with
// none of the headers the handler set, and logs the panic with its stack at the error level. A
// response that has already started cannot change its status: Recover logs the panic and aborts
// the response instead, so the client sees a broken response rather than one that looks complete.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			before := w.Header().Clone()
			rec := &recorder{ResponseWriter: w}
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					// A deliberate abort. The server stays quiet only for the bare sentinel.
					panic(http.ErrAbortHandler)
				}
				log.LogAttrs(r.Context(), slog.LevelError, "handler panicked",
					slog.Any("panic", p),
					slog.Bool("response_started", rec.code != 0),
					slog.String("stack", string(debug.Stack())))
				if rec.code != 0 {
					panic(http.ErrAbortHandler)
				}
				h := w.Header()
				clear(h)
				maps.Copy(h, before)
				writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// BasicAuth returns middleware that requires the HTTP basic credentials user and pass. A request
// without them, or with any others, gets a 401 unauthorized envelope with a WWW-Authenticate
// challenge, and never reaches next. The credentials are compared as SHA-256 digests in constant
// time, so how long the check takes says nothing about how much of them was right, or how long
// they are.
func BasicAuth(user, pass string) func(http.Handler) http.Handler {
	wantUser, wantPass := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, p, ok := r.BasicAuth()
			gotUser, gotPass := sha256.Sum256([]byte(u)), sha256.Sum256([]byte(p))
			// Both comparisons always run, so a right user name takes no longer to refuse.
			match := subtle.ConstantTimeCompare(gotUser[:], wantUser[:]) &
				subtle.ConstantTimeCompare(gotPass[:], wantPass[:])
			if !ok || match != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="solpay-shop admin"`)
				writeError(w, http.StatusUnauthorized, codeUnauthorized, "admin credentials required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// recorder notes the status and the size of the body of a response.
type recorder struct {
	http.ResponseWriter
	code  int // 0 until the status is sent
	bytes int64
}

func (rec *recorder) WriteHeader(code int) {
	// Called first, so a status that the writer refuses with a panic is never recorded as sent.
	rec.ResponseWriter.WriteHeader(code)
	// An informational status other than 101 is not the response's status: another one follows it.
	if rec.code == 0 && (code >= http.StatusOK || code == http.StatusSwitchingProtocols) {
		rec.code = code
	}
}

func (rec *recorder) Write(b []byte) (int, error) {
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	n, err := rec.ResponseWriter.Write(b)
	rec.bytes += int64(n)
	return n, err
}

// FlushError flushes the underlying writer. A flush sends the header, so it starts the response,
// with status 200 if the handler set none. http.ResponseController calls it for Flush.
func (rec *recorder) FlushError() error {
	if rec.code == 0 {
		rec.code = http.StatusOK
	}
	return http.NewResponseController(rec.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the underlying writer, for deadlines and the like.
func (rec *recorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }

// status returns the response's status. A handler that sends nothing gets 200 from the server.
func (rec *recorder) status() int {
	if rec.code == 0 {
		return http.StatusOK
	}
	return rec.code
}
