package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/aleksiuno/solpay-shop/internal/order"
	"github.com/aleksiuno/solpay-shop/internal/product"
)

// Codes of the JSON error envelope. Clients may rely on them: a code never changes its meaning.
const (
	codeInvalidRequest    = "invalid_request"
	codeValidationFailed  = "validation_failed"
	codeNotFound          = "not_found"
	codeMethodNotAllowed  = "method_not_allowed"
	codeProductInactive   = "product_inactive"
	codeSlugTaken         = "slug_taken"
	codeInvalidTransition = "invalid_transition"
	codeUnauthorized      = "unauthorized"
	codeForbidden         = "forbidden"
	codeInternal          = "internal"
)

// errorBody is the JSON error envelope: {"error": {"code": "...", "message": "..."}}. The code is
// for programs and the message for people.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// writeError writes the error envelope with status, code and message.
func writeError(w http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code, body.Error.Message = code, message
	writeJSON(w, status, body)
}

// writeJSON writes v as JSON with status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// "&" stays as it is, not "\u0026", so that a URL in the answer can be copied from it. The
	// headers keep a browser from reading the answer as HTML.
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	// An error here means the client has gone; there is no one left to tell.
	_ = enc.Encode(v)
}

// fail answers err, which a service returned, with the envelope that fits it. An error it does not
// know is a 500 internal envelope, and is logged instead of sent.
func (a api) fail(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *order.ValidationError
	var invalidProduct *product.ValidationError
	switch {
	case errors.As(err, &invalid):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, invalid.Field+" "+invalid.Reason)
	case errors.As(err, &invalidProduct):
		writeError(w, http.StatusUnprocessableEntity, codeValidationFailed, invalidProduct.Field+" "+invalidProduct.Reason)
	case errors.Is(err, product.ErrSlugTaken):
		writeError(w, http.StatusConflict, codeSlugTaken, "another product has this slug")
	case errors.Is(err, order.ErrNoLatePayment):
		writeError(w, http.StatusConflict, codeInvalidTransition, "an expired order can be refunded only after a late payment")
	case errors.Is(err, order.ErrInvalidTransition):
		writeError(w, http.StatusConflict, codeInvalidTransition, "the order's status does not allow it")
	case errors.Is(err, order.ErrStaleState):
		writeError(w, http.StatusConflict, codeInvalidTransition, "the order changed meanwhile; read it again")
	case errors.Is(err, product.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "no such product")
	case errors.Is(err, order.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "no such order")
	case errors.Is(err, order.ErrProductInactive):
		writeError(w, http.StatusConflict, codeProductInactive, "the product is not for sale")
	case r.Context().Err() != nil:
		// The client hung up, or the server is stopping: nothing went wrong in the shop, and no one
		// reads the answer.
		a.Log.LogAttrs(r.Context(), slog.LevelWarn, "request canceled",
			slog.Any("error", err), slog.Any("cause", context.Cause(r.Context())))
		writeError(w, http.StatusInternalServerError, codeInternal, "request canceled")
	default:
		a.Log.LogAttrs(r.Context(), slog.LevelError, "request failed", slog.Any("error", err))
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}

// Forbidden answers with a 403 forbidden envelope. It is meant as the deny handler of
// http.CrossOriginProtection.
func Forbidden(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusForbidden, codeForbidden, "cross-origin request refused")
}
