package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const jsonContentType = "application/json; charset=utf-8"

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Version string `json:"version"`
}

type uploadResponse struct {
	Status        string `json:"status"`
	BytesReceived int64  `json:"bytes_received"`
	DurationNs    int64  `json:"duration_ns"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// withCommonHeaders applies headers that must hold for every GoSpeed
// response. no-store keeps proxies and caches from serving a cached payload
// that the client did not measure.
func withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Cache-Control", "no-store, no-cache, must-revalidate")
		header.Set("Pragma", "no-cache")
		header.Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Service: ServiceName,
		Version: s.cfg.Version,
	})
}

// handlePing answers with an empty 204 so the client measures nothing but the
// request/response round trip.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDownload streams incompressible random data. The response is either
// byte limited (with an exact Content-Length) or time limited.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	query := r.URL.Query()
	var byteLimit int64
	var duration time.Duration
	if raw := query.Get("bytes"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			writeError(w, http.StatusBadRequest, "bytes must be a positive integer")
			return
		}
		if value > s.cfg.MaxDownloadBytes {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("bytes exceeds the server limit of %d", s.cfg.MaxDownloadBytes))
			return
		}
		byteLimit = value
	}
	if raw := query.Get("duration_ms"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			writeError(w, http.StatusBadRequest, "duration_ms must be a positive integer")
			return
		}
		duration = time.Duration(value) * time.Millisecond
		if duration > s.cfg.MaxDownloadDuration {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("duration_ms exceeds the server limit of %d", s.cfg.MaxDownloadDuration.Milliseconds()))
			return
		}
	}
	if byteLimit == 0 && duration == 0 {
		duration = s.cfg.DefaultDownloadDuration
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	if byteLimit > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(byteLimit, 10))
	}
	w.WriteHeader(http.StatusOK)

	start := time.Now()
	var sent int64
	for {
		if r.Context().Err() != nil {
			// The client cancelled or disconnected; stop writing immediately.
			return
		}
		if byteLimit > 0 && sent >= byteLimit {
			return
		}
		if duration > 0 && time.Since(start) >= duration {
			return
		}
		chunk := s.downloadBlock
		if byteLimit > 0 {
			if remaining := byteLimit - sent; remaining < int64(len(chunk)) {
				chunk = chunk[:remaining]
			}
		}
		if _, err := w.Write(chunk); err != nil {
			return
		}
		sent += int64(len(chunk))
		if flusher, ok := w.(http.Flusher); ok {
			// Flush each chunk so the client can measure progress instead of
			// waiting for the response to finish.
			flusher.Flush()
		}
	}
}

// handleUpload streams the request body to io.Discard, counts the bytes that
// actually arrived and acknowledges them. Nothing is written to disk.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if r.ContentLength > s.cfg.MaxUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("upload exceeds the server limit of %d bytes", s.cfg.MaxUploadBytes))
		return
	}
	body := http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	start := time.Now()
	received, err := io.Copy(io.Discard, body)
	duration := time.Since(start)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("upload exceeds the server limit of %d bytes", s.cfg.MaxUploadBytes))
			return
		}
		if r.Context().Err() != nil {
			// The client went away; there is nobody left to answer.
			return
		}
		writeError(w, http.StatusBadRequest, "upload interrupted: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, uploadResponse{
		Status:        "ok",
		BytesReceived: received,
		DurationNs:    duration.Nanoseconds(),
	})
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		// Marshalling our own response structs cannot fail in practice, but a
		// broken encoding must never produce a partial 200 response.
		http.Error(w, `{"error":"internal encoding error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Error: message})
}
