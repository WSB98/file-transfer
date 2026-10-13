package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/WSB98/file-transfer/internal/store"
)

// apiError is a failure as the client sees it: a status and a short code.
type apiError struct {
	status int
	code   string
}

func (e *apiError) Error() string { return e.code }

// Failures the server finds itself, before the store is involved.
var (
	errBadRequest = &apiError{http.StatusBadRequest, "bad_request"}
	errBadIndex   = &apiError{http.StatusBadRequest, "bad_index"}
	errBadLength  = &apiError{http.StatusBadRequest, "bad_length"}
	errBadHash    = &apiError{http.StatusBadRequest, "bad_hash"}
	errBadKey     = &apiError{http.StatusUnauthorized, "bad_key"}
	errBusy       = &apiError{http.StatusServiceUnavailable, "busy"}
	errInternal   = &apiError{http.StatusInternalServerError, "internal"}
)

// storeErrors is the one place store failures become HTTP answers.
var storeErrors = []struct {
	err error
	api *apiError
}{
	{store.ErrInvalidSize, &apiError{http.StatusBadRequest, "bad_request"}},
	{store.ErrTooLarge, &apiError{http.StatusRequestEntityTooLarge, "too_large"}},
	{store.ErrTooMany, &apiError{http.StatusTooManyRequests, "too_many"}},
	{store.ErrCannotResume, &apiError{http.StatusConflict, "cannot_resume"}},
	{store.ErrNoSpace, &apiError{http.StatusInsufficientStorage, "cannot_reserve"}},
	{store.ErrUnknownUpload, &apiError{http.StatusNotFound, "unknown_upload"}},
	{store.ErrChunkInFlight, &apiError{http.StatusConflict, "chunk_in_flight"}},
	{store.ErrNotReceiving, &apiError{http.StatusConflict, "not_receiving"}},
	{store.ErrMissingChunks, &apiError{http.StatusConflict, "missing_chunks"}},
	{store.ErrHashMismatch, &apiError{http.StatusUnprocessableEntity, "hash_mismatch"}},
	{store.ErrInterrupted, &apiError{http.StatusRequestTimeout, "interrupted"}},
}

// fail answers with the error's status and code. Anything unexpected is
// logged, and the client only sees "internal".
func (s *Server) fail(w http.ResponseWriter, err error) {
	ae := s.apiErrorFor(err)
	writeJSON(w, ae.status, map[string]string{"error": ae.code})
}

func (s *Server) apiErrorFor(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	for _, m := range storeErrors {
		if errors.Is(err, m.err) {
			return m.api
		}
	}
	s.logf("internal error: %v", err)
	return errInternal
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
