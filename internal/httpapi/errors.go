package httpapi

import (
	"encoding/json"
	"net/http"
)

// Codes of the JSON error envelope. Clients may rely on them: a code never changes its meaning.
const (
	codeNotFound     = "not_found"
	codeUnauthorized = "unauthorized"
	codeForbidden    = "forbidden"
	codeInternal     = "internal"
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
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// An error here means the client has gone; there is no one left to tell.
	_ = json.NewEncoder(w).Encode(body)
}

// Forbidden answers with a 403 forbidden envelope. It is meant as the deny handler of
// http.CrossOriginProtection.
func Forbidden(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusForbidden, codeForbidden, "cross-origin request refused")
}
