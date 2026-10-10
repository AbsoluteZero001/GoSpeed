// Command p0g-mock-server is a localhost-only HTTP fixture for the Cloudflare
// P0-G WebView2 lifecycle tests. It never contacts the public network.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const defaultMaxBytes int64 = 80 * 1024 * 1024

type scenarioCounter struct {
	Started   int `json:"started"`
	Completed int `json:"completed"`
	Aborted   int `json:"aborted"`
	InFlight  int `json:"inFlight"`
}

type stats struct {
	mu              sync.Mutex
	scenarios       map[string]*scenarioCounter
	resultsRequests int
}

type controlCommand struct {
	ID       int    `json:"id"`
	Scenario string `json:"scenario"`
}

type p0gControl struct {
	mu      sync.Mutex
	nextID  int
	queue   []controlCommand
	results map[int]json.RawMessage
}

func newP0GControl() *p0gControl {
	return &p0gControl{results: make(map[int]json.RawMessage)}
}

func (c *p0gControl) enqueue(scenario string) controlCommand {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	command := controlCommand{ID: c.nextID, Scenario: scenario}
	c.queue = append(c.queue, command)
	return command
}

func (c *p0gControl) pop() controlCommand {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return controlCommand{}
	}
	command := c.queue[0]
	c.queue = c.queue[1:]
	return command
}

func (c *p0gControl) store(id int, result json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.results[id] = result
}

func (c *p0gControl) result(id int) (json.RawMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, ok := c.results[id]
	return result, ok
}

func newStats() *stats {
	return &stats{scenarios: make(map[string]*scenarioCounter)}
}

func (s *stats) start(scenario string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counter := s.counterLocked(scenario)
	counter.Started++
	counter.InFlight++
}

func (s *stats) finish(scenario string, aborted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counter := s.counterLocked(scenario)
	if counter.InFlight > 0 {
		counter.InFlight--
	}
	if aborted {
		counter.Aborted++
	} else {
		counter.Completed++
	}
}

func (s *stats) resultRequest() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resultsRequests++
}

func (s *stats) counterLocked(scenario string) *scenarioCounter {
	counter := s.scenarios[scenario]
	if counter == nil {
		counter = &scenarioCounter{}
		s.scenarios[scenario] = counter
	}
	return counter
}

func (s *stats) snapshot(scenario string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	counter := s.scenarios[scenario]
	if counter == nil {
		counter = &scenarioCounter{}
	}
	return map[string]any{
		"scenario":        scenario,
		"started":         counter.Started,
		"completed":       counter.Completed,
		"aborted":         counter.Aborted,
		"inFlight":        counter.InFlight,
		"resultsRequests": s.resultsRequests,
		"localOnly":       true,
	}
}

type fixture struct {
	logger   *log.Logger
	stats    *stats
	control  *p0gControl
	maxBytes int64
}

func corsHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Private-Network", "true")
	w.Header().Set("Access-Control-Expose-Headers", "Retry-After, Server-Timing")
	w.Header().Set("Timing-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
}

func queryInt(values map[string][]string, key string, fallback int) int {
	raw := ""
	if list := values[key]; len(list) > 0 {
		raw = list[0]
	}
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func scenarioOf(values map[string][]string, fallback string) string {
	if list := values["scenario"]; len(list) > 0 && strings.TrimSpace(list[0]) != "" {
		return strings.TrimSpace(list[0])
	}
	return fallback
}

func (f *fixture) logRequest(
	method string,
	path string,
	scenario string,
	started time.Time,
	status int,
	readBytes int64,
	writtenBytes int64,
	aborted bool,
) {
	f.logger.Printf(
		"time=%s method=%s path=%s scenario=%s status=%d duration_ms=%.3f request_bytes=%d response_bytes=%d aborted=%t",
		time.Now().UTC().Format(time.RFC3339Nano),
		method,
		path,
		scenario,
		status,
		float64(time.Since(started).Microseconds())/1000,
		readBytes,
		writtenBytes,
		aborted,
	)
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corsHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	values := r.URL.Query()
	scenario := scenarioOf(values, "normal")
	if strings.HasPrefix(r.URL.Path, "/__p0g/") {
		f.handleP0GControl(w, r)
		return
	}
	switch r.URL.Path {
	case "/health":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
		return
	case "/__stats":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.stats.snapshot(scenario))
		return
	case "/__results":
		f.stats.resultRequest()
		w.WriteHeader(http.StatusOK)
		return
	case "/__down":
		f.handleDownload(w, r, scenario)
		return
	case "/__up":
		f.handleUpload(w, r, scenario)
		return
	default:
		http.NotFound(w, r)
		return
	}
}

func (f *fixture) handleP0GControl(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/__p0g/command":
		scenario := scenarioOf(r.URL.Query(), "")
		if scenario == "" {
			http.Error(w, "scenario is required", http.StatusBadRequest)
			return
		}
		command := f.control.enqueue(scenario)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(command)
		return
	case "/__p0g/next":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.control.pop())
		return
	case "/__p0g/result":
		if r.Method == http.MethodPost {
			id, err := strconv.Atoi(r.URL.Query().Get("id"))
			if err != nil || id <= 0 {
				http.Error(w, "valid id is required", http.StatusBadRequest)
				return
			}
			body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
			if err != nil {
				http.Error(w, "read result: "+err.Error(), http.StatusBadRequest)
				return
			}
			if len(body) == 0 || !json.Valid(body) {
				http.Error(w, "result must be valid JSON", http.StatusBadRequest)
				return
			}
			f.control.store(id, json.RawMessage(body))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id, err := strconv.Atoi(r.URL.Query().Get("id"))
		if err != nil || id <= 0 {
			http.Error(w, "valid id is required", http.StatusBadRequest)
			return
		}
		result, ok := f.control.result(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(result)
		return
	default:
		http.NotFound(w, r)
	}
}

func (f *fixture) handleDownload(w http.ResponseWriter, r *http.Request, scenario string) {
	started := time.Now()
	bytes := queryInt(r.URL.Query(), "bytes", 0)
	f.stats.start(scenario)

	if bytes > int(f.maxBytes) {
		f.stats.finish(scenario, false)
		http.Error(w, "requested bytes exceed local fixture limit", http.StatusRequestEntityTooLarge)
		f.logRequest(r.Method, r.URL.Path, scenario, started, http.StatusRequestEntityTooLarge, 0, 0, false)
		return
	}

	delayMs := queryInt(r.URL.Query(), "delay_ms", 20)
	if bytes == 0 {
		delayMs = 0
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(bytes))
	w.Header().Set("Server-Timing", "cfSpeedEdge;dur=0")

	if bytes == 0 {
		w.WriteHeader(http.StatusOK)
		f.stats.finish(scenario, false)
		f.logRequest(r.Method, r.URL.Path, scenario, started, http.StatusOK, 0, 0, false)
		return
	}

	if scenario == "per_request_timeout" || scenario == "cancel" || scenario == "close" {
		w.WriteHeader(http.StatusOK)
		flush(w)
		_, _ = w.Write([]byte{0})
		flush(w)
		<-r.Context().Done()
		f.stats.finish(scenario, true)
		f.logRequest(r.Method, r.URL.Path, scenario, started, http.StatusOK, 0, 1, true)
		return
	}

	if scenario == "overall_timeout" {
		delayMs = 12_000
	}
	if delayMs > 0 {
		select {
		case <-time.After(time.Duration(delayMs) * time.Millisecond):
		case <-r.Context().Done():
			f.stats.finish(scenario, true)
			f.logRequest(r.Method, r.URL.Path, scenario, started, http.StatusOK, 0, 0, true)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	written, err := io.CopyN(w, zeroReader{}, int64(bytes))
	aborted := err != nil || r.Context().Err() != nil
	f.stats.finish(scenario, aborted)
	f.logRequest(r.Method, r.URL.Path, scenario, started, http.StatusOK, 0, written, aborted)
}

func (f *fixture) handleUpload(w http.ResponseWriter, r *http.Request, scenario string) {
	started := time.Now()
	f.stats.start(scenario)
	body := http.MaxBytesReader(w, r.Body, f.maxBytes)
	readBytes, err := io.Copy(io.Discard, body)
	if err != nil {
		f.stats.finish(scenario, r.Context().Err() != nil)
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "http: request body too large") {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid upload fixture body", status)
		f.logRequest(r.Method, r.URL.Path, scenario, started, status, readBytes, 0, r.Context().Err() != nil)
		return
	}

	if scenario == "per_request_timeout" || scenario == "cancel" || scenario == "close" {
		w.WriteHeader(http.StatusOK)
		flush(w)
		<-r.Context().Done()
		f.stats.finish(scenario, true)
		f.logRequest(r.Method, r.URL.Path, scenario, started, http.StatusOK, readBytes, 0, true)
		return
	}

	delayMs := queryInt(r.URL.Query(), "delay_ms", 20)
	if scenario == "overall_timeout" {
		delayMs = 12_000
	}
	if delayMs > 0 {
		select {
		case <-time.After(time.Duration(delayMs) * time.Millisecond):
		case <-r.Context().Done():
			f.stats.finish(scenario, true)
			f.logRequest(r.Method, r.URL.Path, scenario, started, http.StatusOK, readBytes, 0, true)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	f.stats.finish(scenario, false)
	f.logRequest(r.Method, r.URL.Path, scenario, started, http.StatusOK, readBytes, 0, false)
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18080", "localhost listen address")
	logPath := flag.String("log", "", "optional local log file")
	maxBytes := flag.Int64("max-bytes", defaultMaxBytes, "maximum request or response body bytes")
	flag.Parse()

	var output io.Writer = os.Stdout
	var logFile *os.File
	if strings.TrimSpace(*logPath) != "" {
		file, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			log.Fatalf("open log: %v", err)
		}
		logFile = file
		defer logFile.Close()
		output = io.MultiWriter(os.Stdout, file)
	}

	server := &http.Server{
		Addr: *addr,
		Handler: &fixture{
			logger:   log.New(output, "", 0),
			stats:    newStats(),
			control:  newP0GControl(),
			maxBytes: *maxBytes,
		},
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(output, "p0g mock server listening on http://%s (local only)\n", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("mock server: %v", err)
	}
}
