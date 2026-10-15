// Package provider is the WAN-01-A NDT7 adapter. It wraps the official
// github.com/m-lab/ndt7-client-go SDK into a single Run() call that the
// WAN-01-A tests drive, and it surfaces the protocol contract that the
// future GoSpeed Provider integration must honour.
//
// The adapter is intentionally thin. It does not redo the SDK's work; it
// pins the SDK version and the user-agent fields, runs one direction at a
// time, and emits a structured result. It enforces a hard wire-level byte
// budget at the goroutine that owns the socket, because the official SDK
// v0.10.1 carries only time-based controls (DownloadTimeout / UploadTimeout /
// IOTimeout). The P0-I report "byte budget" section and the SDK API surface
// confirm this: no public parameter accepts a byte cap.
package provider

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

const (
	// ClientName / ClientVersion are the User-Agent suffix the SDK sends.
	// They identify this build as the GoSpeed WAN-01-A PoC and never
	// impersonate the official M-Lab client.
	ClientName    = "GoSpeed-wan01a"
	ClientVersion = "0.1.0-wan01a"

	// SubprotocolRequired is the exact Sec-WebSocket-Protocol value the
	// SDK sends. Tests assert this value at the mock server.
	SubprotocolRequired = "net.measurementlab.ndt.v7"
)

// Direction names the protocol direction of one measurement run.
type Direction string

const (
	DirectionDownload Direction = "download"
	DirectionUpload   Direction = "upload"
)

// Status is the terminal state of one measurement.
type Status string

const (
	StatusCompleted Status = "completed"
	StatusPartial   Status = "partial"
	StatusCancelled Status = "cancelled"
	StatusTimeout   Status = "timeout"
	StatusFailed    Status = "failed"
)

// ErrorClass is a coarse-grained failure category.
type ErrorClass string

const (
	ErrorNone       ErrorClass = "none"
	ErrorHandshake  ErrorClass = "handshake"
	ErrorNetwork    ErrorClass = "network"
	ErrorTimeout    ErrorClass = "timeout"
	ErrorCancelled  ErrorClass = "cancelled"
	ErrorServerDown ErrorClass = "server_down"
	ErrorConfig     ErrorClass = "config"
)

// Progress is one update from the SDK's measurement channel.
type Progress struct {
	Direction Direction
	Origin    string
	Elapsed   time.Duration
	Bytes     int64
	Final     bool
}

// Result is the structured outcome of one direction.
type Result struct {
	Direction  Direction  `json:"direction"`
	Status     Status     `json:"status"`
	ErrorClass ErrorClass `json:"errorClass"`
	Error      string     `json:"error,omitempty"`
	Bytes      int64      `json:"bytes"`
	ElapsedMs  int64      `json:"elapsedMs"`
	Mbps       float64    `json:"mbps"`
	// WireBytesRead / WireBytesWritten are the bytes the local socket
	// observed. They bound the application payload from above because
	// WebSocket framing overhead is non-negative, but they may include
	// framing and TLS record overhead too. They are NOT ISP billing
	// bytes and NOT a peer-attested server-side count.
	WireBytesRead    int64 `json:"wireBytesRead"`
	WireBytesWritten int64 `json:"wireBytesWritten"`
	// BudgetLimitBytes is the wire-level byte budget the adapter applied.
	// 0 means unlimited.
	BudgetLimitBytes int64 `json:"budgetLimitBytes"`
	// BudgetExceeded is true iff the budget triggered and the run ended
	// with status partial rather than completed.
	BudgetExceeded bool `json:"budgetExceeded"`
	// SubprotocolObserved is what the SDK actually negotiated. WAN-01-A
	// tests pin it to net.measurementlab.ndt.v7.
	SubprotocolObserved string `json:"subprotocolObserved"`
	// FinalMeasurementObserved records whether at least one measurement
	// JSON frame arrived with the Origin marker the SDK documents as
	// "server".
	FinalMeasurementObserved bool `json:"finalMeasurementObserved"`
	// ServerName records the server-side hostname when the SDK provides
	// it through the ConnectionInfo frame.
	ServerName string `json:"serverName"`
}

// Options configures one run.
type Options struct {
	// Server is host:port or a ws://host:port/path URL. Path and query are
	// preserved when present; the adapter only overrides the scheme.
	Server string
	// Scheme is the WebSocket scheme: ws (mock only) or wss (production).
	Scheme string
	// Direction selects download or upload.
	Direction Direction
	// OverallTimeout is the wall-clock cap. The SDK also carries internal
	// DownloadTimeout / UploadTimeout / IOTimeout; the adapter does not
	// touch them and lets the SDK keep its defaults (15s / 10s / 7s).
	OverallTimeout time.Duration
	// WireBudgetBytes caps bytes observed at the local socket. 0 = off.
	// This is the experimental PoC budget from the WAN-01-A task; it does
	// not replace the SDK's time-based controls.
	WireBudgetBytes int64
	// OnProgress, when set, is invoked for every measurement event the
	// SDK emits. It must return quickly and never block.
	OnProgress func(Progress)
}

// Adapter wraps the official SDK so each WAN-01-A test can drive one run
// with deterministic inputs and observe the SDK's contract.
type Adapter struct {
	options Options
}

// New returns an Adapter for the given options.
func New(options Options) (*Adapter, error) {
	if options.Server == "" {
		return nil, errors.New("server is required")
	}
	switch options.Direction {
	case DirectionDownload, DirectionUpload:
	default:
		return nil, fmt.Errorf("unsupported direction %q", options.Direction)
	}
	switch options.Scheme {
	case "", "ws", "wss":
	default:
		return nil, fmt.Errorf("unsupported scheme %q", options.Scheme)
	}
	if options.OverallTimeout < 0 {
		return nil, errors.New("overall timeout must be non-negative")
	}
	if options.WireBudgetBytes < 0 {
		return nil, errors.New("wire budget must be non-negative")
	}
	return &Adapter{options: options}, nil
}

// serviceURL builds the URL the SDK dials. The official SDK has no public
// helper for "swap http(s) for ws(s) and append /ndt/v7/<direction>", so
// the adapter derives it from the configured Server.
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
		return "", fmt.Errorf("invalid server %q: %w", raw, err)
	}
	parsed.Scheme = scheme
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ndt/v7/" + string(a.options.Direction)
	return parsed.String(), nil
}

// Run performs one measurement. It returns a fully populated Result and a
// terminal error. A non-nil error does not imply "no evidence": the Status
// distinguishes completed / partial / cancelled / timeout / failed.
//
// Run installs a goroutine-tracked wire budget. Whenever the budget is hit
// the underlying WebSocket connection is force-closed; this is the only way
// to interrupt blocked SDK I/O within the P0-I observed cancellation
// latency of ~1 ms (active) / ~7 s (idle / I/O deadline).
func (a *Adapter) Run(parentCtx context.Context) (Result, error) {
	result := Result{Direction: a.options.Direction, Status: StatusFailed, ErrorClass: ErrorConfig}
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
		// WAN-01-A does NOT set Subprotocols here. The SDK adds its own
		// Sec-WebSocket-Protocol header internally (the constant
		// params.SecWebSocketProtocol in the SDK, which the mock asserts);
		// duplicating it here would cause the gorilla/websocket Dialer
		// to send two Sec-WebSocket-Protocol header values, which the
		// server (and the mock) reject with a handshake error.

		NetDialContext: a.dialHook(registry),
	}

	client := ndt7.NewClient(ClientName, ClientVersion)
	// Pin the user agent and the per-call dialer / service URL on the
	// SDK's public fields. The SDK's own Locate path is bypassed by
	// setting ServiceURL (which takes precedence over Locate).
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
		result.SubprotocolObserved = SubprotocolRequired
		result.ErrorClass = classifyStartError(err)
		result.Error = err.Error()
		result.Status = StatusFailed
		registry.forceCloseAll()
		return result, err
	}

	start := time.Now()
	var lastAppBytes int64
	var serverName string
	var sawServerOrigin bool
	for m := range ch {
		if m.Origin == spec.OriginServer {
			sawServerOrigin = true
		}
		if m.AppInfo != nil {
			lastAppBytes = m.AppInfo.NumBytes
		}
		if m.ConnectionInfo != nil && m.ConnectionInfo.Server != "" {
			serverName = m.ConnectionInfo.Server
		}
		if a.options.OnProgress != nil {
			a.options.OnProgress(Progress{
				Direction: a.options.Direction,
				Origin:    string(m.Origin),
				Elapsed:   time.Since(start),
				Bytes:     lastAppBytes,
				Final:     m.Origin == spec.OriginServer,
			})
		}
		if registry.exceeded() {
			break
		}
	}
	elapsed := time.Since(start)

	result.SubprotocolObserved = SubprotocolRequired
	result.ServerName = serverName
	result.WireBytesRead = registry.bytesRead()
	result.WireBytesWritten = registry.bytesWritten()
	result.Bytes = lastAppBytes
	result.ElapsedMs = elapsed.Milliseconds()
	result.BudgetLimitBytes = a.options.WireBudgetBytes
	result.BudgetExceeded = registry.exceeded()
	// FinalMeasurementObserved records whether the SDK ever observed a
	// server-origin measurement frame. The last measurement the SDK emits
	// is a client-origin update (the final 250 ms tick), so we track
	// the highest-priority origin we saw during the run.
	result.FinalMeasurementObserved = sawServerOrigin

	switch {
	case registry.exceeded():
		result.Status = StatusPartial
		result.ErrorClass = ErrorNone
		result.Error = "wire byte budget exceeded"
		return result, nil
	case errors.Is(ctx.Err(), context.Canceled) && parentCtx.Err() != nil:
		result.Status = StatusCancelled
		result.ErrorClass = ErrorCancelled
		result.Error = "context cancelled"
		return result, nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded) && a.options.OverallTimeout > 0:
		result.Status = StatusTimeout
		result.ErrorClass = ErrorTimeout
		result.Error = "overall timeout"
		return result, nil
	}
	result.Status = StatusCompleted
	if lastAppBytes == 0 {
		result.Status = StatusFailed
		result.ErrorClass = ErrorNetwork
		result.Error = "no application bytes received"
		return result, fmt.Errorf("no application bytes received")
	}
	result.Mbps = mbpsFromBytes(lastAppBytes, elapsed)
	return result, nil
}

// mbpsFromBytes returns the goodput in megabits per second.
func mbpsFromBytes(bytes int64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(bytes) * 8 / elapsed.Seconds() / 1e6
}

// classifyStartError maps a StartDownload / StartUpload error onto a coarse
// error class. gorilla/websocket v1.5.4 returns a plain errors.New when the
// server answers an HTTP error code; it only returns a *websocket.HandshakeError
// for protocol-specific handshake failures. Both cases mean the server did
// not accept the WebSocket upgrade, so this classifier treats them as a
// single ErrorHandshake class.
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
	if errMsg := err.Error(); strings.Contains(errMsg, "websocket: bad handshake") ||
		strings.Contains(errMsg, "websocket: protocol error") ||
		strings.Contains(errMsg, "websocket: unsupported protocol") {
		return ErrorHandshake
	}
	return ErrorServerDown
}

// dialHook returns the NetDialContext the SDK uses to dial. It tracks every
// dialed net.Conn through the wire budget registry so the adapter can
// count bytes and force-close on overrun.
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
// through it. When the configured budget is exceeded it force-closes the
// connection so blocked SDK I/O returns immediately.
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

// hit records a budget overrun and force-closes the underlying connection.
func (r *registry) hit() {
	if r.budgetExceeded.CompareAndSwap(false, true) {
		r.forceCloseAll()
	}
}

// trackedConn wraps a net.Conn and counts bytes.
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

// HandshakeErrorType is exported so tests can type-assert the SDK's
// underlying handshake error without importing gorilla/websocket twice.
var HandshakeErrorType = (*websocket.HandshakeError)(nil)

// HTTPResponseType exposes the SDK's underlying HTTP transport response
// inspection type so tests can confirm the mock server answers in the
// same way the official server does (e.g. on a 4xx Locate response).
type HTTPResponseType = http.Response
