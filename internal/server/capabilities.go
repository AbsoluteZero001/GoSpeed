package server

import (
	"net/http"
	"time"
)

// capabilitiesResponse is the GET /capabilities payload. Clients use it to
// learn the protocol version, the server version and the limits they must
// respect before starting a measurement.
type capabilitiesResponse struct {
	ProtocolVersion int                `json:"protocol_version"`
	ServerVersion   string             `json:"server_version"`
	Capabilities    []string           `json:"capabilities"`
	Limits          capabilitiesLimits `json:"limits"`
}

type capabilitiesLimits struct {
	MaxConnectionsPerTest int   `json:"max_connections_per_test"`
	MaxConcurrentTests    int   `json:"max_concurrent_tests"`
	MaxDurationSeconds    int   `json:"max_duration_seconds"`
	MaxDownloadBytes      int64 `json:"max_download_bytes"`
	MaxUploadBytes        int64 `json:"max_upload_bytes"`
}

// testCapabilities lists the measurement endpoints this server implements.
var testCapabilities = []string{"latency", "download", "upload"}

// handleCapabilities is additive: a v0.2.0 client ignores it, and a v0.3.0
// client falls back gracefully when an older server answers 404.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, capabilitiesResponse{
		ProtocolVersion: ProtocolVersion,
		ServerVersion:   s.cfg.Version,
		Capabilities:    testCapabilities,
		Limits: capabilitiesLimits{
			MaxConnectionsPerTest: s.cfg.MaxConnectionsPerTest,
			MaxConcurrentTests:    s.cfg.MaxConcurrentTests,
			MaxDurationSeconds:    maxTestDurationSeconds(s.cfg),
			MaxDownloadBytes:      s.cfg.MaxDownloadBytes,
			MaxUploadBytes:        s.cfg.MaxUploadBytes,
		},
	})
}

// maxTestDurationSeconds is the longest client test duration the server is
// willing to serve: bounded by both the download duration limit and the HTTP
// read timeout, rounded down to whole seconds (never below one).
func maxTestDurationSeconds(cfg Config) int {
	limit := cfg.MaxDownloadDuration
	if cfg.ReadTimeout > 0 && cfg.ReadTimeout < limit {
		limit = cfg.ReadTimeout
	}
	seconds := int(limit / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}
