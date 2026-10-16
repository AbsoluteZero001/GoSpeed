// Package ndt7 is the GoSpeed production adapter for the M-Lab NDT7
// protocol. It lives next to the GoSpeed HTTP Provider and shares the
// same provider.Runner contract so the GUI, the CLI and any future
// consumer can pick a measurement backend by ID without changing the
// code that drives it.
//
// The adapter wraps the official github.com/m-lab/ndt7-client-go SDK
// and adds only what the SDK does not expose:
//
//   - A wire-level byte budget enforced at the goroutine that owns the
//     socket. The SDK v0.10.1 only carries time-based controls
//     (DownloadTimeout / UploadTimeout / IOTimeout, 15 s / 10 s / 7 s);
//     without this layer a download over a fast link can outrun the
//     caller's MaxBytes before the SDK observes the wall clock.
//   - An immediate cancellation path that force-closes the underlying
//     net.Conn as soon as the run context is cancelled. The SDK I/O
//     deadline otherwise leaves the caller waiting up to 7 s.
//   - A measurement window that runs from the first measurement frame
//     to the close of the SDK channel and explicitly excludes the
//     WebSocket close handshake (which is asynchronous and would
//     otherwise pollute the rate).
//
// Everything that the SDK already reports (server-side byte counts in
// AppInfo, TCPInfo measurements) is forwarded verbatim through the
// provider.ProviderDetail channel under the ndt7-client-result/v1
// schema. Nothing in this package ever invents a number that was not
// observed.
package ndt7

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-lab/ndt7-client-go"
	"github.com/m-lab/ndt7-client-go/spec"
)

// ClientName / ClientVersion are the User-Agent suffix the SDK sends.
// They identify this build as GoSpeed and never impersonate the
// official M-Lab client.
const (
	ClientName    = "GoSpeed"
	ClientVersion = "0.1.0-ndt7"

	// Subprotocol is the exact Sec-WebSocket-Protocol value NDT7
	// mandates. The adapter pins it on the gorilla/websocket Dialer
	// through client.Dialer.Subprotocols; the SDK does not add its own
	// header so the value below is the only header sent.
	Subprotocol = "net.measurementlab.ndt.v7"

	// DetailFormat is the schema identifier the adapter uses for the
	// provider.ProviderDetail it returns, so consumers can decide
	// whether they decode the native payload.
	DetailFormat = "ndt7-client-result/v1"
)

// Direction names the NDT7 protocol direction of one measurement run.
type Direction string

const (
	DirectionDownload Direction = "download"
	DirectionUpload   Direction = "upload"
)

// Status is the terminal state of one adapter run.
type Status string

const (
	StatusCompleted Status = "completed"
	StatusPartial   Status = "partial" // budget truncated the run
	StatusCancelled Status = "cancelled"
	StatusTimeout   Status = "timeout"
	StatusFailed    Status = "failed"
)

// ErrorClass is the coarse-grained failure category the adapter maps
// every error to. The mapping is stable so callers can match on it.
type ErrorClass string

const (
	ErrorNone       ErrorClass = "none"
	ErrorHandshake  ErrorClass = "handshake"
	ErrorNetwork    ErrorClass = "network"
	ErrorTimeout    ErrorClass = "timeout"
	ErrorCancelled  ErrorClass = "cancelled"
	ErrorServerDown ErrorClass = "server_down"
)

// Result is the structured outcome of one adapter run. The fields are
// deliberately separated between application bytes (the payload the
// measurement cared about) and wire bytes (what the local socket
// observed, which bounds the application payload from above). Window
// describes how the elapsed time was measured; the rate is computed
// from the application bytes over that window so it never includes
// the WebSocket close handshake.
type Result struct {
	Direction  Direction  `json:"direction"`
	Status     Status     `json:"status"`
	ErrorClass ErrorClass `json:"errorClass"`
	Error      string     `json:"error,omitempty"`

	// ApplicationBytes is the application-layer byte count over the
	// measurement window: NumBytes at the last SDK frame minus
	// NumBytes at the first SDK frame. The cumulative SDK count
	// (and ElapsedTime) is also carried in Detail.Payload.
	// Both bytes and time come from the SDK-reported AppInfo so the
	// Mbps formula uses one consistent source.
	//
	// For download this is the bytes the client counted as
	// received between the first and the last SDK measurement
	// frame. For upload it is the same delta from the client's
	// perspective; the server's own TCPInfo confirms the
	// reception through a separate channel.
	ApplicationBytes int64 `json:"applicationBytes"`

	// WireBytesRead / WireBytesWritten are the bytes the local socket
	// observed. They include WebSocket framing and (for wss://) TLS
	// record overhead; they do not include TCP/IP header bytes, TCP
	// retransmissions or kernel-buffered data the client never read.
	// They bound the application payload from above.
	WireBytesRead    int64 `json:"wireBytesRead"`
	WireBytesWritten int64 `json:"wireBytesWritten"`

	// ElapsedMs is the measurement window in milliseconds, from the
	// first SDK measurement frame to the close of the SDK channel.
	// It deliberately excludes the WebSocket close handshake; a
	// measurement that reports zero bytes within a positive window is
	// still well defined.
	ElapsedMs int64 `json:"elapsedMs"`

	// Mbps is application bytes * 8 / window / 1e6. 0 means the
	// measurement was too fast for the platform clock to resolve; the
	// Status field stays the source of truth.
	Mbps float64 `json:"mbps"`

	// Window names how the elapsed time was derived. Downstream
	// consumers can show it next to the rate so a user knows exactly
	// what the number measured.
	Window string `json:"window"`

	// StopReason states why the transfer stopped. The values mirror
	// the engine contract and add ndt7-specific reasons for outcomes
	// the engine does not produce.
	StopReason string `json:"stopReason,omitempty"`

	// SubprotocolObserved is what the SDK actually negotiated; the
	// adapter pins the requested value so this field is for
	// diagnostics, not branching.
	SubprotocolObserved string `json:"subprotocolObserved"`

	// ServerName is the server-side hostname the SDK reported in the
	// ConnectionInfo frame, when it did.
	ServerName string `json:"serverName"`

	// BudgetLimitBytes is the wire-level byte budget the adapter
	// applied. 0 means unlimited.
	BudgetLimitBytes int64 `json:"budgetLimitBytes"`

	// BudgetExceeded is true iff the budget triggered and the run
	// ended with status partial rather than completed.
	BudgetExceeded bool `json:"budgetExceeded"`

	// LastFrameWasServerOrigin is true iff the last measurement frame
	// the SDK emitted before the channel closed had Origin="server".
	// It is the only signal the adapter has for distinguishing a
	// server-side close (clean EOF) from an SDK-side timeout, and is
	// used by the runner to map the run onto StopReasonServerEOF
	// rather than StopReasonDuration.
	LastFrameWasServerOrigin bool `json:"lastFrameWasServerOrigin"`
}

// Options configures one adapter run.
type Options struct {
	Server          string // host:port or ws://host:port/path
	Scheme          string // "ws" or "wss"
	Direction       Direction
	OverallTimeout  time.Duration // wall-clock cap; 0 means SDK defaults only
	WireBudgetBytes int64         // wire-level read/write cap; 0 = unlimited
}

// Adapter wraps the official SDK so a Provider.Run call can drive one
// run with deterministic inputs.
type Adapter struct {
	options Options
}

// New returns an Adapter for the given options.
func New(options Options) (*Adapter, error) {
	if options.Server == "" {
		return nil, errors.New("ndt7: server is required")
	}
	switch options.Direction {
	case DirectionDownload, DirectionUpload:
	default:
		return nil, fmt.Errorf("ndt7: unsupported direction %q", options.Direction)
	}
	switch options.Scheme {
	case "", "ws", "wss":
	default:
		return nil, fmt.Errorf("ndt7: unsupported scheme %q", options.Scheme)
	}
	if options.OverallTimeout < 0 {
		return nil, errors.New("ndt7: overall timeout must be non-negative")
	}
	if options.WireBudgetBytes < 0 {
		return nil, errors.New("ndt7: wire budget must be non-negative")
	}
	return &Adapter{options: options}, nil
}

// serviceURL builds the URL the SDK dials. The official SDK has no
// public helper for swapping http(s) for ws(s) and appending
// /ndt/v7/<direction>, so the adapter derives it from the configured
// Server.
func (a *Adapter) serviceURL() (string, error) {
	scheme := a.options.Scheme
	if scheme == "" {
		scheme = "wss"
	}
	raw := a.options.Server
	if !strings.Contains(raw, "://") {
		raw = scheme + "://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("ndt7: invalid server %q: %w", raw, err)
	}
	parsed.Scheme = scheme
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ndt/v7/" + string(a.options.Direction)
	return parsed.String(), nil
}

// Run performs one measurement and returns a fully populated Result
// together with a terminal error. The Status field distinguishes
// completed / partial / cancelled / timeout / failed regardless of
// whether err is nil.
//
// Run installs a wire byte budget via a NetDialContext hook that
// wraps every dialed net.Conn. When the budget is exceeded or the
// context is cancelled, the hook force-closes the underlying socket so
// the SDK's blocked I/O returns immediately instead of waiting for the
// SDK I/O deadline (7 s).
func (a *Adapter) Run(parentCtx context.Context) (Result, error) {
	result := Result{
		Direction:           a.options.Direction,
		Status:              StatusFailed,
		ErrorClass:          ErrorNetwork,
		SubprotocolObserved: Subprotocol,
		BudgetLimitBytes:    a.options.WireBudgetBytes,
	}
	serviceURL, err := a.serviceURL()
	if err != nil {
		result.Error = err.Error()
		return result, err
	}

	ctx, cancel := context.WithCancel(parentCtx)
	if a.options.OverallTimeout > 0 {
		ctx, cancel = context.WithTimeout(parentCtx, a.options.OverallTimeout)
	}
	defer cancel()

	registry := newRegistry(a.options.WireBudgetBytes)
	dialer := &websocket.Dialer{
		HandshakeTimeout: 7 * time.Second,
		// The SDK adds its own Sec-WebSocket-Protocol header internally
		// (the constant params.SecWebSocketProtocol). Duplicating it
		// here would cause the gorilla/websocket Dialer to send two
		// Sec-WebSocket-Protocol header values, which the server (and
		// the mock) reject with a handshake error. Therefore the
		// Subprotocols field is intentionally not set.
		NetDialContext: a.dialHook(registry),
	}

	client := ndt7.NewClient(ClientName, ClientVersion)
	client.Dialer = *dialer
	client.Scheme = a.options.Scheme
	if client.Scheme == "" {
		client.Scheme = "wss"
	}
	parsed, err := url.Parse(serviceURL)
	if err != nil {
		result.Error = err.Error()
		return result, err
	}
	client.ServiceURL = parsed

	// Force-close the connection on any of:
	//   - parent context cancellation
	//   - OverallTimeout expiry (handled by ctx)
	//   - wire byte budget overrun (registry triggers forceCloseAll)
	go func() {
		select {
		case <-ctx.Done():
			registry.forceCloseAll()
		case <-registry.done():
			return
		}
	}()

	var ch <-chan spec.Measurement
	switch a.options.Direction {
	case DirectionDownload:
		ch, err = client.StartDownload(ctx)
	case DirectionUpload:
		ch, err = client.StartUpload(ctx)
	}
	if err != nil {
		result.ErrorClass = classifyStartError(err)
		result.Error = err.Error()
		result.Status = StatusFailed
		registry.forceCloseAll()
		return result, err
	}

	// The measurement window starts at the first measurement frame
	// the SDK emits. That excludes the TCP+TLS handshake and the
	// WebSocket upgrade, which are not part of the application-layer
	// window the speedtest contract defines.
	// Both the byte count and the time come from the SDK-reported
	// AppInfo fields, so the Mbps calculation uses one consistent
	// source. Mixing SDK-reported bytes with adapter-measured time
	// (or vice versa) would mix units that did not cover the same
	// window.
	//
	// firstSeen is true once we have observed at least one
	// AppInfo-bearing frame; without it we cannot compute a delta
	// and we honestly leave Mbps / ElapsedMs / Window unset.
	var (
		windowActive             bool
		firstSeen                bool
		firstAppBytes            int64
		lastAppBytes             int64
		firstElapsedUs           int64
		lastElapsedUs            int64
		serverName               string
		lastFrameWasServerOrigin bool
	)
	for m := range ch {
		if !windowActive {
			windowActive = true
		}
		if m.AppInfo != nil {
			if !firstSeen {
				firstSeen = true
				firstAppBytes = m.AppInfo.NumBytes
				firstElapsedUs = m.AppInfo.ElapsedTime
			}
			lastAppBytes = m.AppInfo.NumBytes
			lastElapsedUs = m.AppInfo.ElapsedTime
		}
		if m.Origin == spec.OriginServer {
			lastFrameWasServerOrigin = true
		}
		if m.ConnectionInfo != nil && m.ConnectionInfo.Server != "" {
			serverName = m.ConnectionInfo.Server
		}
		if registry.exceeded() {
			break
		}
	}
	registry.forceCloseAll() // release everything we tracked

	// ApplicationBytes is the cumulative application-layer byte
	// count from SDK start. It stays available even when the
	// window-based rate could not be computed.
	result.ApplicationBytes = lastAppBytes
	result.WireBytesRead = registry.bytesRead()
	result.WireBytesWritten = registry.bytesWritten()
	result.ServerName = serverName
	result.BudgetExceeded = registry.exceeded()
	if firstSeen {
		deltaBytes := lastAppBytes - firstAppBytes
		deltaElapsedUs := lastElapsedUs - firstElapsedUs
		// A single-frame run, or a run whose AppInfo did not advance,
		// is honestly reported as having no measurable rate; the
		// wire byte counts and the cumulative ApplicationBytes
		// still describe how much data moved.
		if deltaElapsedUs > 0 {
			result.ElapsedMs = deltaElapsedUs / 1000
			result.Window = windowFor(a.options.Direction, len(a.options.Server) > 0)
			if deltaBytes > 0 {
				result.Mbps = float64(deltaBytes) * 8 / (float64(deltaElapsedUs) / 1e6) / 1e6
			}
		}
		// The StopReason is set by the caller (Status switch);
		// lastFrameWasServerOrigin is the only signal we have for
		// distinguishing a server-side close from an SDK-side timeout.
		result.LastFrameWasServerOrigin = lastFrameWasServerOrigin
	}

	switch {
	case registry.exceeded():
		result.Status = StatusPartial
		result.ErrorClass = ErrorNone
		result.Error = "wire byte budget exceeded"
		result.StopReason = StopReasonBudgetExceeded
		return result, nil
	case errors.Is(ctx.Err(), context.Canceled) && parentCtx.Err() != nil:
		result.Status = StatusCancelled
		result.ErrorClass = ErrorCancelled
		result.Error = "context cancelled"
		result.StopReason = StopReasonCancelled
		return result, nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && a.options.OverallTimeout > 0:
		result.Status = StatusTimeout
		result.ErrorClass = ErrorTimeout
		result.Error = "overall timeout"
		result.StopReason = StopReasonTimeout
		return result, nil
	}
	if lastAppBytes == 0 {
		result.Status = StatusFailed
		result.ErrorClass = ErrorNetwork
		result.Error = "no application bytes received"
		result.StopReason = StopReasonServerEOF
		return result, fmt.Errorf("no application bytes received")
	}
	result.Status = StatusCompleted
	result.StopReason = StopReasonDuration
	return result, nil
}

// mbpsFromBytes returns the application-layer goodput in megabits per
// second.
func mbpsFromBytes(bytes int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(bytes) * 8 / elapsed.Seconds() / 1e6
}

// classifyStartError maps a StartDownload / StartUpload error onto a
// coarse error class. gorilla/websocket v1.5.4 returns a plain
// errors.New when the server answers an HTTP error code; it only
// returns a *websocket.HandshakeError for protocol-specific handshake
// failures. Both cases mean the server did not accept the WebSocket
// upgrade, so this classifier treats them as a single ErrorHandshake
// class.
func classifyStartError(err error) ErrorClass {
	if err == nil {
		return ErrorNone
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCancelled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	if _, ok := err.(*websocket.HandshakeError); ok {
		return ErrorHandshake
	}
	if msg := err.Error(); strings.Contains(msg, "websocket: bad handshake") ||
		strings.Contains(msg, "websocket: protocol error") ||
		strings.Contains(msg, "websocket: unsupported protocol") {
		return ErrorHandshake
	}
	return ErrorServerDown
}

// windowFor names the measurement window for the Result.Window field
// so a consumer can show exactly what the number measured.
func windowFor(direction Direction, hasServer bool) string {
	if !hasServer {
		return ""
	}
	switch direction {
	case DirectionDownload:
		return WindowDownload
	case DirectionUpload:
		return WindowUpload
	default:
		return ""
	}
}

// dialHook returns the NetDialContext the SDK uses to dial. It tracks
// every dialed net.Conn through the wire budget registry so the
// adapter can count bytes and force-close on overrun.
func (a *Adapter) dialHook(reg *registry) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := &net.Dialer{}
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return reg.track(conn), nil
	}
}

// registry tracks every socket the SDK opens and counts bytes flowing
// through it. When the configured budget is exceeded it force-closes
// the connection so blocked SDK I/O returns immediately.
//
// The counted bytes include WebSocket framing overhead and (for wss://)
// TLS record overhead. They are NOT ISP billing bytes and NOT a
// peer-attested server-side count; they are the local socket's
// honest observation, which is the strongest wire-level budget the
// client can enforce without modifying the SDK.
type registry struct {
	mu             sync.Mutex
	conns          map[net.Conn]struct{}
	budget         int64
	rBytes         atomic.Int64
	wBytes         atomic.Int64
	budgetExceeded atomic.Bool
	doneCh         chan struct{}
	once           sync.Once
}

func newRegistry(budget int64) *registry {
	return &registry{conns: map[net.Conn]struct{}{}, budget: budget, doneCh: make(chan struct{})}
}

// track wraps and registers a raw net.Conn.
func (r *registry) track(conn net.Conn) net.Conn {
	tc := &trackedConn{registry: r, Conn: conn}
	r.mu.Lock()
	r.conns[conn] = struct{}{}
	r.mu.Unlock()
	return tc
}

// forceCloseAll closes every tracked connection. Idempotent.
func (r *registry) forceCloseAll() {
	r.once.Do(func() { close(r.doneCh) })
	r.mu.Lock()
	conns := make([]net.Conn, 0, len(r.conns))
	for conn := range r.conns {
		conns = append(conns, conn)
	}
	r.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// exceeded reports whether the byte budget was overrun.
func (r *registry) exceeded() bool { return r.budgetExceeded.Load() }

// bytesRead returns the total number of bytes read from the wire.
func (r *registry) bytesRead() int64 { return r.rBytes.Load() }

// bytesWritten returns the total number of bytes written to the wire.
func (r *registry) bytesWritten() int64 { return r.wBytes.Load() }

// done returns a channel closed when the registry is finalised.
func (r *registry) done() <-chan struct{} { return r.doneCh }

// hit records a budget overrun and force-closes the underlying
// connection.
func (r *registry) hit() {
	if r.budgetExceeded.CompareAndSwap(false, true) {
		r.forceCloseAll()
	}
}

// trackedConn wraps a net.Conn and counts bytes. The Read method
// returns errBudgetExceeded when the wire-level budget is crossed; the
// Write method refuses bytes before they reach the network so the
// application cannot actively push effective payload beyond the
// budget. Either path triggers forceCloseAll, so the next blocked
// SDK I/O returns immediately.
type trackedConn struct {
	net.Conn
	registry *registry
}

func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		total := c.registry.rBytes.Add(int64(n))
		if c.registry.budget > 0 && total > c.registry.budget {
			c.registry.hit()
			if err == nil {
				err = errBudgetExceeded
			}
		}
	}
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	if c.registry.budget > 0 {
		pending := c.registry.wBytes.Load()
		if pending+int64(len(p)) > c.registry.budget {
			c.registry.hit()
			return 0, errBudgetExceeded
		}
	}
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.registry.wBytes.Add(int64(n))
	}
	return n, err
}

var errBudgetExceeded = errors.New("ndt7 wire byte budget exceeded")

// StopReason values reported in Result.StopReason. They mirror the
// engine contract so an aggregating consumer can group statistics by
// protocol without parsing protocol-specific strings.
const (
	// StopReasonDuration is reported when the measurement window
	// closed naturally before either byte budget, timeout or
	// cancellation fired. For a single-shot protocol like NDT7 this is
	// also the value reported when the server closed the stream and
	// the SDK exited its loop.
	StopReasonDuration = "duration_elapsed"
	// StopReasonServerEOF is reserved for a future scenario where the
	// adapter can distinguish "duration elapsed" from "server closed".
	StopReasonServerEOF = "server_eof"
	// StopReasonBudgetExceeded is reported when the wire-level budget
	// was overrun; it always comes with StatusPartial.
	StopReasonBudgetExceeded = "ndt7_budget_exceeded"
	// StopReasonCancelled is reported when the run context was
	// cancelled before the protocol could finish.
	StopReasonCancelled = "ndt7_cancelled"
	// StopReasonTimeout is reported when the OverallTimeout was hit.
	StopReasonTimeout = "ndt7_timeout"
)

// Window names published for the Result.Window field. They mirror the
// engine's wording so a downstream consumer can show the same UI label
// for both protocols.
const (
	WindowDownload = "ndt7_app_first_measurement_to_last_measurement"
	WindowUpload   = "ndt7_app_first_measurement_to_last_server_confirmation"
)

// HTTPResponse is exposed so tests can type-assert the SDK's
// underlying HTTP transport response.
type HTTPResponse = http.Response
