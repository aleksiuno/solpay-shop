package httpapi

import (
	"context"
	"log/slog"
)

// requestIDKey is the context key under which RequestID stores a request's id.
type requestIDKey struct{}

// requestID returns the id that RequestID gave the request ctx belongs to, or "" outside one.
func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// ContextHandler is a slog.Handler that adds a request_id attribute to every record logged with
// the context of a request that RequestID has seen, so the lines logged while serving a request,
// the access log line among them, can be matched to it and to its X-Request-ID header. Records
// logged with any other context pass through unchanged.
//
// The attribute goes in the innermost group of the logger, as any attribute a record carries does.
type ContextHandler struct {
	slog.Handler
}

// Handle adds request_id to r when ctx carries one, then passes r on.
func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := requestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs keeps the handler a ContextHandler, so a logger made with With still adds request_id.
func (h ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return ContextHandler{h.Handler.WithAttrs(attrs)}
}

// WithGroup keeps the handler a ContextHandler, as WithAttrs does.
func (h ContextHandler) WithGroup(name string) slog.Handler {
	return ContextHandler{h.Handler.WithGroup(name)}
}
