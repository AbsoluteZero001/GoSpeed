package speedtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

// ServerLimits is the subset of server limits the engine checks before it
// starts measuring. Zero values mean "not advertised".
type ServerLimits struct {
	MaxConnectionsPerTest int   `json:"max_connections_per_test,omitempty"`
	MaxConcurrentTests    int   `json:"max_concurrent_tests,omitempty"`
	MaxDurationSeconds    int   `json:"max_duration_seconds,omitempty"`
	MaxDownloadBytes      int64 `json:"max_download_bytes,omitempty"`
	MaxUploadBytes        int64 `json:"max_upload_bytes,omitempty"`
}

// CapabilityInfo records the outcome of GET /capabilities. Supported=false
// with a non-empty Error distinguishes "the server does not implement the
// endpoint" from "the server could not be asked"; neither case fabricates
// capabilities.
type CapabilityInfo struct {
	Supported bool `json:"supported"`
	// Unsupported is true when the server answered 404/405: a v0.2.0 server
	// without capability negotiation. The run continues.
	Unsupported     bool          `json:"unsupported_endpoint,omitempty"`
	ProtocolVersion int           `json:"protocol_version,omitempty"`
	ServerVersion   string        `json:"server_version,omitempty"`
	Capabilities    []string      `json:"capabilities,omitempty"`
	Limits          *ServerLimits `json:"limits,omitempty"`
	Error           string        `json:"error,omitempty"`
}

// fetchCapabilities asks the optional /capabilities endpoint. A 404/405 is a
// normal answer from a v0.2.0 server and never fails the run.
func fetchCapabilities(ctx context.Context, client *http.Client, target Target) CapabilityInfo {
	info := CapabilityInfo{}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.BaseURL+"/capabilities", nil)
	if err != nil {
		info.Error = fmt.Sprintf("build request: %v", err)
		return info
	}
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", version.UserAgent())
	response, err := client.Do(request)
	if err != nil {
		info.Error = fmt.Sprintf("request failed: %v", err)
		return info
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
	}()
	switch response.StatusCode {
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		info.Unsupported = true
		info.Error = "server does not implement GET /capabilities"
		return info
	case http.StatusOK:
	default:
		info.Error = fmt.Sprintf("unexpected capabilities status: %s", response.Status)
		return info
	}
	var payload struct {
		ProtocolVersion int           `json:"protocol_version"`
		ServerVersion   string        `json:"server_version"`
		Capabilities    []string      `json:"capabilities"`
		Limits          *ServerLimits `json:"limits"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&payload); err != nil {
		info.Error = fmt.Sprintf("invalid capabilities response: %v", err)
		return info
	}
	info.Supported = true
	info.ProtocolVersion = payload.ProtocolVersion
	info.ServerVersion = payload.ServerVersion
	info.Capabilities = payload.Capabilities
	info.Limits = payload.Limits
	return info
}

// validateOptions refuses a request the server explicitly said it will not
// serve, instead of letting the server reject individual connections halfway
// through a test. It never clamps silently.
func (c CapabilityInfo) validateOptions(options Options) error {
	if !c.Supported || c.Limits == nil {
		return c.validateCapabilities(options)
	}
	limits := c.Limits
	if limits.MaxConnectionsPerTest > 0 && options.Connections > limits.MaxConnectionsPerTest {
		return fmt.Errorf("%w: server %s allows at most %d connections per test, requested %d",
			ErrServerLimitExceeded, c.ServerVersion, limits.MaxConnectionsPerTest, options.Connections)
	}
	if limits.MaxDurationSeconds > 0 && options.Duration > time.Duration(limits.MaxDurationSeconds)*time.Second {
		return fmt.Errorf("%w: server %s allows a test duration of at most %ds, requested %s",
			ErrServerLimitExceeded, c.ServerVersion, limits.MaxDurationSeconds, options.Duration)
	}
	if limits.MaxDownloadBytes > 0 && options.MaxBytes > limits.MaxDownloadBytes {
		return fmt.Errorf("%w: server %s allows at most %d download bytes per request, requested %d",
			ErrServerLimitExceeded, c.ServerVersion, limits.MaxDownloadBytes, options.MaxBytes)
	}
	if limits.MaxUploadBytes > 0 && options.MaxBytes > limits.MaxUploadBytes {
		return fmt.Errorf("%w: server %s allows at most %d upload bytes per request, requested %d",
			ErrServerLimitExceeded, c.ServerVersion, limits.MaxUploadBytes, options.MaxBytes)
	}
	return c.validateCapabilities(options)
}

// validateCapabilities checks that the server advertises the phases the run
// asks for. A server that does not implement /capabilities is not judged.
func (c CapabilityInfo) validateCapabilities(options Options) error {
	if !c.Supported || len(c.Capabilities) == 0 {
		return nil
	}
	supported := make(map[string]bool, len(c.Capabilities))
	for _, name := range c.Capabilities {
		supported[strings.ToLower(name)] = true
	}
	missing := make([]string, 0, 3)
	if options.Phases&PhasesLatency != 0 && !supported["latency"] {
		missing = append(missing, "latency")
	}
	if options.Phases&PhasesDownload != 0 && !supported["download"] {
		missing = append(missing, "download")
	}
	if options.Phases&PhasesUpload != 0 && !supported["upload"] {
		missing = append(missing, "upload")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: server %s does not advertise: %s",
			ErrServerCapabilityMissing, c.ServerVersion, strings.Join(missing, ", "))
	}
	return nil
}
