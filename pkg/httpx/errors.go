// Package httpx holds the HTTP helpers shared by all services:
// the JSON error format, JSON reading and writing, middleware and a server with graceful shutdown.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// ErrorBody is the one error shape every service returns:
//
//	{ "error": { "code": "clan_full", "message": "Clan has reached 10 members" } }
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inside of ErrorBody. Code is stable and machine-readable; Message is for people.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is an error that knows its HTTP status and code. Return it from service code
// and the handler can turn it into a response with WriteErr.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// NewError builds an *Error.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteError writes the standard error body.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, ErrorBody{Error: ErrorDetail{Code: code, Message: message}})
}

// WriteErr writes err as the standard error body. An *Error keeps its status and code;
// anything else becomes a 500 with a generic message, so internal details never leak.
func WriteErr(w http.ResponseWriter, err error) {
	var e *Error
	if errors.As(err, &e) {
		WriteError(w, e.Status, e.Code, e.Message)
		return
	}
	WriteError(w, http.StatusInternalServerError, "internal_error", "Something went wrong")
}

// MaxBodyBytes is the largest JSON body DecodeJSON accepts (1 MB).
const MaxBodyBytes = 1 << 20

// DecodeJSON reads a JSON body into dst. It rejects bodies over MaxBodyBytes,
// unknown fields and trailing data, and returns an *Error with code invalid_json.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return NewError(http.StatusBadRequest, "invalid_json", "Request body is not valid JSON: "+err.Error())
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return NewError(http.StatusBadRequest, "invalid_json", "Request body must contain a single JSON object")
	}
	return nil
}
