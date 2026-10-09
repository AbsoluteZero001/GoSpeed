// Package server implements the self hosted GoSpeed HTTP test endpoints.
//
// The server is deliberately small and safe by default: it only exposes fixed
// endpoints, never proxies or forwards anything, never reads or writes files
// on behalf of a client, and listens on loopback unless the operator opts into
// a wider address explicitly.
package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

// ServiceName identifies the server in health responses.
const ServiceName = "gospeed"

// ProtocolVersion is the GoSpeed test protocol version advertised by
// GET /capabilities. It changes only when the HTTP contract changes in a way a
// client must know about.
const ProtocolVersion = 1

// downloadBlockSize is the size of one write to the response body and the size
// of the shared random block. A block that is larger than one write keeps the
// generated data incompressible.
const downloadBlockSize = 64 << 10

// Config configures the HTTP test server.
type Config struct {
	// Addr is the listen address. The default keeps the server on loopback.
	Addr string
	// MaxUploadBytes bounds one upload request body.
	MaxUploadBytes int64
	// MaxDownloadBytes bounds one /download response.
	MaxDownloadBytes int64
	// MaxDownloadDuration bounds one /download response that is not byte
	// limited.
	MaxDownloadDuration time.Duration
	// MaxConcurrentTests bounds how many /download and /upload requests are
	// served at the same time. Further requests receive 503 so a single client
	// cannot exhaust the host.
	MaxConcurrentTests int
	// MaxConnectionsPerTest is the server's advisory limit for parallel
	// connections inside one test. It is advertised through /capabilities.
	MaxConnectionsPerTest int
	// DefaultDownloadDuration is used when a request sets neither bytes nor
	// duration_ms.
	DefaultDownloadDuration time.Duration
	ReadHeaderTimeout       time.Duration
	ReadTimeout             time.Duration
	WriteTimeout            time.Duration
	IdleTimeout             time.Duration
	// Version is reported by /health. It defaults to the build version.
	Version string
}

// DefaultConfig returns the safe defaults used by the CLI.
func DefaultConfig() Config {
	return Config{
		Addr:                    "127.0.0.1:8080",
		MaxUploadBytes:          1 << 30,
		MaxDownloadBytes:        4 << 30,
		MaxDownloadDuration:     60 * time.Second,
		MaxConcurrentTests:      32,
		MaxConnectionsPerTest:   16,
		DefaultDownloadDuration: 10 * time.Second,
		ReadHeaderTimeout:       5 * time.Second,
		ReadTimeout:             60 * time.Second,
		WriteTimeout:            75 * time.Second,
		IdleTimeout:             120 * time.Second,
		Version:                 version.Version,
	}
}

// Server owns the handler and the shared random download block.
type Server struct {
	cfg           Config
	downloadBlock []byte
	slots         chan struct{}
}

// New validates the configuration, fills unset fields with defaults and
// prepares the random download block.
func New(cfg Config) (*Server, error) {
	cfg = withDefaults(cfg)
	if err := validate(cfg); err != nil {
		return nil, err
	}
	block := make([]byte, downloadBlockSize)
	if _, err := rand.Read(block); err != nil {
		return nil, fmt.Errorf("server: generate download payload: %w", err)
	}
	return &Server{cfg: cfg, downloadBlock: block, slots: make(chan struct{}, cfg.MaxConcurrentTests)}, nil
}

// Config returns the effective configuration.
func (s *Server) Config() Config {
	return s.cfg
}

// Handler returns the HTTP handler with the test API mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ping", s.handlePing)
	mux.HandleFunc("/download", s.handleDownload)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/capabilities", s.handleCapabilities)
	return withCommonHeaders(mux)
}

// acquire takes one concurrent test slot. It reports false when the server is
// already serving MaxConcurrentTests requests.
func (s *Server) acquire() bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) release() {
	<-s.slots
}

// Listen binds the configured address. Callers that need the resolved address
// (for example when the port is zero) should use this instead of
// ListenAndServe.
func (s *Server) Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("server: listen on %s: %w", s.cfg.Addr, err)
	}
	return listener, nil
}

// ListenAndServe listens on the configured address and serves until ctx is
// cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	listener, err := s.Listen()
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

// Serve serves on listener until ctx is cancelled and then shuts down
// gracefully.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	httpServer := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: s.cfg.ReadHeaderTimeout,
		ReadTimeout:       s.cfg.ReadTimeout,
		WriteTimeout:      s.cfg.WriteTimeout,
		IdleTimeout:       s.cfg.IdleTimeout,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("server: shutdown: %w", err)
		}
		<-serveErr
		return nil
	case err := <-serveErr:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server: serve: %w", err)
	}
}

func withDefaults(cfg Config) Config {
	defaults := DefaultConfig()
	if cfg.Addr == "" {
		cfg.Addr = defaults.Addr
	}
	if cfg.MaxUploadBytes == 0 {
		cfg.MaxUploadBytes = defaults.MaxUploadBytes
	}
	if cfg.MaxDownloadBytes == 0 {
		cfg.MaxDownloadBytes = defaults.MaxDownloadBytes
	}
	if cfg.MaxDownloadDuration == 0 {
		cfg.MaxDownloadDuration = defaults.MaxDownloadDuration
	}
	if cfg.MaxConcurrentTests == 0 {
		cfg.MaxConcurrentTests = defaults.MaxConcurrentTests
	}
	if cfg.MaxConnectionsPerTest == 0 {
		cfg.MaxConnectionsPerTest = defaults.MaxConnectionsPerTest
	}
	if cfg.DefaultDownloadDuration == 0 {
		cfg.DefaultDownloadDuration = defaults.DefaultDownloadDuration
	}
	if cfg.ReadHeaderTimeout == 0 {
		cfg.ReadHeaderTimeout = defaults.ReadHeaderTimeout
	}
	if cfg.ReadTimeout == 0 {
		cfg.ReadTimeout = defaults.ReadTimeout
	}
	if cfg.WriteTimeout == 0 {
		cfg.WriteTimeout = defaults.WriteTimeout
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = defaults.IdleTimeout
	}
	if cfg.Version == "" {
		cfg.Version = defaults.Version
	}
	if cfg.DefaultDownloadDuration > cfg.MaxDownloadDuration {
		cfg.DefaultDownloadDuration = cfg.MaxDownloadDuration
	}
	return cfg
}

func validate(cfg Config) error {
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return fmt.Errorf("server: invalid addr %q: %w", cfg.Addr, err)
	}
	if cfg.MaxUploadBytes <= 0 {
		return fmt.Errorf("server: max upload bytes must be positive")
	}
	if cfg.MaxDownloadBytes <= 0 {
		return fmt.Errorf("server: max download bytes must be positive")
	}
	if cfg.MaxDownloadDuration <= 0 {
		return fmt.Errorf("server: max download duration must be positive")
	}
	if cfg.MaxConcurrentTests <= 0 {
		return fmt.Errorf("server: max concurrent tests must be positive")
	}
	if cfg.MaxConnectionsPerTest <= 0 {
		return fmt.Errorf("server: max connections per test must be positive")
	}
	if cfg.DefaultDownloadDuration <= 0 {
		return fmt.Errorf("server: default download duration must be positive")
	}
	if cfg.ReadHeaderTimeout < 0 {
		return fmt.Errorf("server: read header timeout must not be negative")
	}
	if cfg.ReadTimeout < 0 {
		return fmt.Errorf("server: read timeout must not be negative")
	}
	if cfg.WriteTimeout < 0 {
		return fmt.Errorf("server: write timeout must not be negative")
	}
	if cfg.IdleTimeout < 0 {
		return fmt.Errorf("server: idle timeout must not be negative")
	}
	// A write timeout shorter than the longest download would cut valid
	// streams in half and produce a misleading failure.
	if cfg.WriteTimeout > 0 && cfg.WriteTimeout <= cfg.MaxDownloadDuration {
		return fmt.Errorf("server: write timeout %s must exceed max download duration %s",
			cfg.WriteTimeout, cfg.MaxDownloadDuration)
	}
	return nil
}
