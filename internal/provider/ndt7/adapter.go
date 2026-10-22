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

	// ApplicationBytes is the application-layer byte count over
	// the measurement window: NumBytes at the last SDK frame
	// minus NumBytes at the first SDK frame. The cumulative
	// SDK count from SDK start is also reported as
	// CumulativeAppBytes below.
	//
	// Both bytes and time come from the SDK-reported AppInfo so
	// the Mbps formula uses one consistent source. A single
	// frame run, or a run whose AppInfo did not advance, cannot
	// be divided; in that case Mbps stays nil.
	ApplicationBytes int64 `json:"applicationBytes"`

	// CumulativeAppBytes is the cumulative application-layer byte
	// count from SDK start. It stays available even when the
	// window-based rate could not be computed.
	CumulativeAppBytes int64 `json:"cumulativeAppBytes"`

	// CumulativeElapsedUs is the cumulative elapsed time from
	// SDK start in microseconds (matches AppInfo.ElapsedTime).
	CumulativeElapsedUs int64 `json:"cumulativeElapsedUs"`

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

	// ServerOriginFrames counts the measurement frames the SDK tagged
	// Origin="server". Those frames carry the SERVER's own AppInfo
	// (the server-side byte counter and the server-side clock), which
	// is a different quantity from the client-side counter. They are
	// never differenced against client frames; see
	// MeasurementSource.
	//
	// This is a liveness observation only. A server-origin frame is
	// emitted periodically while the test is running (roughly every
	// 250 ms in ndt-server), so seeing one proves the server was
	// alive at some point and says NOTHING about how the test ended.
	// It must never be used as termination evidence.
	ServerOriginFrames int64 `json:"serverOriginFrames"`

	// ServerAppBytes / ServerElapsedUs are the LAST server-origin
	// AppInfo values observed. They are published for transparency and
	// cross-checking only; they are never used as a numerator or a
	// denominator in the Mbps formula, because they come from a
	// different counter and a different clock than the client-side
	// window. ServerAppBytes == 0 means "no server measurement frame
	// was observed", which is a real possibility on a short run.
	ServerAppBytes  int64 `json:"serverAppBytes"`
	ServerElapsedUs int64 `json:"serverElapsedUs"`
	ServerFrameSeen bool  `json:"serverFrameSeen"`

	// MeasurementSource names the single origin whose cumulative
	// AppInfo counter and clock produced ApplicationBytes, ElapsedMs
	// and Mbps. It is one of the MeasurementSource* constants below,
	// or MeasurementSourceUnavailable when no window could be
	// established (single frame, counter regression, time
	// regression or a zero-length window).
	//
	// Both the numerator and the denominator ALWAYS come from this
	// one origin. Mixing origins would divide bytes counted by one
	// party by seconds counted by another.
	MeasurementSource string `json:"measurementSource"`

	// WindowRejectReason explains why no rate was published when
	// MeasurementSource is MeasurementSourceUnavailable. Empty when a
	// window was formed. It exists so a consumer never has to infer
	// "missing" from a zero.
	WindowRejectReason string `json:"windowRejectReason,omitempty"`

	// WindowUs / DeltaBytes expose the raw window arithmetic so a
	// consumer can recompute Mbps from the same numbers the adapter
	// used instead of trusting the rounded result. DeltaBytes is the
	// window byte delta and WindowUs the window duration in
	// microseconds, both from MeasurementSource.
	DeltaBytes int64 `json:"deltaBytes"`
	WindowUs   int64 `json:"windowUs"`

	// FirstAppBytes / FirstElapsedUs and LastAppBytes / LastElapsedUs
	// are the exact client-origin AppInfo samples the window was built
	// from. They are published so a consumer can re-derive DeltaBytes
	// and WindowUs independently and confirm that both endpoints came
	// from the same origin's cumulative series.
	FirstAppBytes  int64 `json:"firstAppBytes"`
	FirstElapsedUs int64 `json:"firstElapsedUs"`
	LastAppBytes   int64 `json:"lastAppBytes"`
	LastElapsedUs  int64 `json:"lastElapsedUs"`
}

// Options configures one adapter run.
type Options struct {
	Server          string // host:port, a base URL, or a full NDT7 Locate URL
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

// serviceURL builds the URL the SDK dials.
//
// The official SDK has no public helper for swapping http(s) for ws(s) and
// pointing at /ndt/v7/<direction>, so the adapter derives it from the
// configured Server.
//
// The path is normalised, never appended to. The SDK (ndt7-client-go
// v0.10.1) accepts a custom service URL only when its path is EXACTLY
// "/ndt/v7/download" or "/ndt/v7/upload"; any other path is refused with
// ErrServiceUnsupported. Appending to the configured path therefore
// produced a doubled path for a full NDT7 Locate URL, which already
// carries /ndt/v7/<direction> plus the access_token query, and the SDK
// rejected the run with "unsupported service url".
//
// Everything except the path is preserved: host, port and the whole query
// string, because a Locate URL carries the access_token that the M-Lab
// server requires. The direction always decides the path, so an input
// that names the other direction is normalised rather than inherited.
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
		// Neither the quoted address nor the wrapped error may carry the
		// access_token of a Locate URL.
		return "", fmt.Errorf("ndt7: invalid server %q: %w", redactServerForLog(raw), redactURLError(err))
	}
	parsed.Scheme = scheme
	// Replace the path instead of appending, so an input that already
	// contains the NDT7 path is normalised rather than doubled.
	parsed.Path = ndt7Path(a.options.Direction)
	// RawPath caches the escaped form of the previous path. Leaving it
	// behind would let url.URL.String() emit a stale escaped path.
	parsed.RawPath = ""
	if parsed.RawQuery == "" {
		parsed.ForceQuery = false
	}
	return parsed.String(), nil
}

// ndt7Path returns the exact URL path the NDT7 protocol uses for one
// direction. The SDK compares this string literally, so the value must
// match its own constants byte for byte.
func ndt7Path(direction Direction) string {
	return "/ndt/v7/" + string(direction)
}

// redactServerForLog removes the parts of a server address that can carry
// a credential: the userinfo component, the whole query string and the
// fragment.
//
// A full NDT7 Locate URL carries the M-Lab access_token in its query. The
// error path must never print it, because the message travels into
// provider.FinalResult.ErrorMessage, which is displayed and persisted.
//
// The whole query is removed rather than a list of known parameter names:
// a deny-list fails open the moment a new parameter name appears.
func redactServerForLog(address string) string {
	if address == "" {
		return address
	}
	parsed, err := url.Parse(address)
	if err != nil {
		// An unparsable address is still an address that may embed a
		// secret, so it is reduced to what cannot be a credential.
		return stripServerUserinfoTextually(address)
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

// redactURLError returns a copy of a *url.Error whose URL field carries no
// credential. net/url puts the whole address into the error message, so a
// Locate URL would otherwise leak its access_token through the wrapped
// error even when the quoted address above is redacted.
//
// The copy keeps the original Op and Err, so errors.Is and errors.As keep
// working exactly as before.
func redactURLError(err error) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}
	redacted := *urlErr
	redacted.URL = redactServerForLog(redacted.URL)
	return &redacted
}

// stripServerUserinfoTextually removes the userinfo, the query and the
// fragment from an address that cannot be parsed.
func stripServerUserinfoTextually(address string) string {
	start := strings.Index(address, "//")
	if start < 0 {
		return address
	}
	rest := address[start+2:]
	hostEnd := strings.IndexAny(rest, "/?#")
	if hostEnd < 0 {
		hostEnd = len(rest)
	}
	host := rest[:hostEnd]
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	tail := rest[hostEnd:]
	if cut := strings.IndexAny(tail, "?#"); cut >= 0 {
		tail = tail[:cut]
	}
	return address[:start+2] + host + tail
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

	// The measurement window spans the FIRST to the LAST
	// client-origin AppInfo frame. Both the byte count and the elapsed
	// time come from those frames, so the numerator and the
	// denominator always cover the same window measured by the same
	// party with the same cumulative counter.
	//
	// Server-origin frames are deliberately EXCLUDED from the window.
	// In ndt7 a server-origin AppInfo is the server's own counter
	// (bytes the server sent or received) against the server's own
	// clock. Differencing a client frame against a server frame would
	// divide one party's byte count by another party's elapsed time,
	// which is not a rate at all. Server frames are recorded in
	// ServerAppBytes / ServerElapsedUs for transparency instead.
	//
	// counterRegressed / timeRegressed latch if the cumulative counter
	// or clock ever moves backwards. AppInfo.NumBytes and
	// AppInfo.ElapsedTime are cumulative from SDK start and therefore
	// monotonic; a regression means the frames are not a coherent
	// series, so the window is refused rather than divided.
	var (
		firstSeen        bool
		firstAppBytes    int64
		lastAppBytes     int64
		firstElapsedUs   int64
		lastElapsedUs    int64
		serverName       string
		serverFrameSeen  bool
		serverAppBytes   int64
		serverElapsedUs  int64
		serverFrames     int64
		counterRegressed bool
		timeRegressed    bool
	)
	for m := range ch {
		switch m.Origin {
		case spec.OriginServer:
			// Liveness only. NOT termination evidence: ndt-server
			// emits these roughly every 250 ms while the test runs.
			serverFrames++
			if m.AppInfo != nil {
				serverAppBytes = m.AppInfo.NumBytes
				serverElapsedUs = m.AppInfo.ElapsedTime
				serverFrameSeen = true
			}
		case spec.OriginClient:
			if m.AppInfo != nil {
				if !firstSeen {
					firstSeen = true
					firstAppBytes = m.AppInfo.NumBytes
					firstElapsedUs = m.AppInfo.ElapsedTime
				} else {
					if m.AppInfo.NumBytes < lastAppBytes {
						counterRegressed = true
					}
					if m.AppInfo.ElapsedTime < lastElapsedUs {
						timeRegressed = true
					}
				}
				lastAppBytes = m.AppInfo.NumBytes
				lastElapsedUs = m.AppInfo.ElapsedTime
			}
		}
		if m.ConnectionInfo != nil && m.ConnectionInfo.Server != "" {
			serverName = m.ConnectionInfo.Server
		}
		if registry.exceeded() {
			break
		}
	}
	registry.forceCloseAll() // release everything we tracked

	// ApplicationBytes is the client-side application byte count over
	// the measurement window: NumBytes at the last client-origin SDK
	// frame minus NumBytes at the first one. Cumulative client values
	// are carried in CumulativeAppBytes / CumulativeElapsedUs so a
	// consumer can recover either view without re-running.
	deltaBytes := int64(0)
	windowUs := int64(0)
	if firstSeen {
		deltaBytes = lastAppBytes - firstAppBytes
		windowUs = lastElapsedUs - firstElapsedUs
	}
	result.ApplicationBytes = deltaBytes
	result.DeltaBytes = deltaBytes
	result.WindowUs = windowUs
	result.CumulativeAppBytes = lastAppBytes
	result.CumulativeElapsedUs = lastElapsedUs
	result.FirstAppBytes = firstAppBytes
	result.FirstElapsedUs = firstElapsedUs
	result.LastAppBytes = lastAppBytes
	result.LastElapsedUs = lastElapsedUs
	result.WireBytesRead = registry.bytesRead()
	result.WireBytesWritten = registry.bytesWritten()
	result.ServerName = serverName
	result.ServerFrameSeen = serverFrameSeen
	result.ServerAppBytes = serverAppBytes
	result.ServerElapsedUs = serverElapsedUs
	result.ServerOriginFrames = serverFrames
	result.BudgetExceeded = registry.exceeded()

	// A rate is published only when a genuine, monotonic, non-empty
	// window exists. Every rejection path below leaves Mbps at 0 (the
	// adapter's "not measured") and records why in MeasurementSource,
	// so the runner never has to guess why a number is missing.
	//
	// - no client frame at all            -> unavailable
	// - exactly one client frame          -> no delta exists
	// - cumulative counter went backwards -> incoherent series
	// - cumulative clock went backwards   -> incoherent series
	// - zero/negative window              -> unresolvable
	//
	// A zero-byte-but-positive window IS publishable: it is a real
	// measurement of zero throughput, not a missing one.
	windowReason := windowRejectReason(firstSeen, counterRegressed, timeRegressed, windowUs)
	if windowReason != "" {
		result.ApplicationBytes = 0
		result.DeltaBytes = 0
		result.WindowUs = 0
		result.ElapsedMs = 0
		result.Mbps = 0
		result.Window = ""
		result.MeasurementSource = MeasurementSourceUnavailable
		result.WindowRejectReason = windowReason
	} else {
		result.ElapsedMs = windowUs / 1000
		result.Window = windowFor(a.options.Direction, len(a.options.Server) > 0)
		result.MeasurementSource = MeasurementSourceClientAppInfo
		if deltaBytes < 0 {
			// Cannot happen without counterRegressed, but keep the
			// invariant local rather than relying on the switch above.
			result.ApplicationBytes = 0
			result.DeltaBytes = 0
			result.Mbps = 0
		} else {
			result.Mbps = mbpsFromBytes(deltaBytes, time.Duration(windowUs)*time.Microsecond)
		}
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
		result.StopReason = StopReasonNoData
		return result, fmt.Errorf("no application bytes received")
	}
	result.Status = StatusCompleted
	if result.MeasurementSource == MeasurementSourceUnavailable {
		// Bytes moved, but no honest rate could be derived. Report the
		// distinct reason so a consumer can tell "measured zero" apart
		// from "not measurable".
		result.StopReason = StopReasonWindowUnavailable
		return result, nil
	}
	// The SDK's StartDownload/StartUpload expose only a channel of
	// measurements; the loop that produces it is started as
	// `go f(ctx, conn, inch)` inside ndt7.Client.collectData and its
	// returned error is DISCARDED. The adapter therefore cannot
	// observe whether the channel closed because the SDK's internal
	// DownloadTimeout/UploadTimeout expired, because the server closed
	// the stream, or because of an abnormal disconnect. It reports the
	// neutral StopReasonCompleted instead of guessing.
	result.StopReason = StopReasonCompleted
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

// windowRejectReason decides whether a client-side measurement window
// can be formed, and if not, why.
//
// It is deliberately a pure function of the four observations that
// determine the answer. The degenerate cases it rejects - a single
// frame, a counter that moved backwards, a clock that moved backwards
// and a zero-length window - are all things a caller can only provoke
// by controlling the SDK's 250 ms UpdateInterval against a real
// socket, i.e. by racing the wall clock. Keeping the decision here
// lets those cases be asserted exhaustively and deterministically in
// an in-package test, with no timing involved at all.
//
// Returning "" means the window is publishable.
func windowRejectReason(firstSeen, counterRegressed, timeRegressed bool, windowUs int64) string {
	switch {
	case !firstSeen:
		return "no client-origin measurement frame was observed"
	case counterRegressed:
		return "the cumulative byte counter moved backwards; the frames are not a coherent series"
	case timeRegressed:
		return "the cumulative elapsed-time counter moved backwards; the frames are not a coherent series"
	case windowUs <= 0:
		return "the measurement window is zero-length; a single client frame carries no delta"
	default:
		return ""
	}
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
	// StopReasonCompleted is the neutral terminal reason for a run that
	// finished with application bytes and was not cut short by a budget,
	// a cancellation or the overall timeout.
	//
	// It is deliberately NOT "duration_elapsed" and NOT "server_eof".
	// ndt7-client-go v0.10.1 starts the protocol loop with
	// `go f(ctx, conn, inch)` in Client.collectData and throws the
	// returned error away, so the only thing the adapter observes is
	// the channel closing. It cannot tell an SDK deadline apart from a
	// server-side close or an abnormal disconnect, so it reports the
	// neutral completion and records the ambiguity in
	// Method.Warnings rather than asserting a cause it never saw.
	StopReasonCompleted = "ndt7_completed"
	// StopReasonServerEOF is NOT emitted by this adapter. It is kept
	// so a consumer that switches on the value has a defined symbol,
	// but ndt7-client-go exposes no server-initiated close event, so
	// producing it would be an unverified claim. See StopReasonCompleted.
	StopReasonServerEOF = "server_eof"
	// StopReasonBudgetExceeded is reported when the wire-level budget
	// was overrun; it always comes with StatusPartial.
	StopReasonBudgetExceeded = "ndt7_budget_exceeded"
	// StopReasonCancelled is reported when the run context was
	// cancelled before the protocol could finish.
	StopReasonCancelled = "ndt7_cancelled"
	// StopReasonTimeout is reported when the OverallTimeout was hit.
	StopReasonTimeout = "ndt7_timeout"
	// StopReasonNoData is reported when the run finished but the
	// client observed zero application bytes.
	StopReasonNoData = "ndt7_no_application_bytes"
	// StopReasonWindowUnavailable is reported when bytes moved but no
	// usable measurement window could be formed (single frame,
	// counter regression, or a zero-length window). The run is a
	// success at the byte level but carries no rate.
	StopReasonWindowUnavailable = "ndt7_window_unavailable"
)

// MeasurementSource values name the single origin that produced the
// rate. Exactly one source is ever used for a given direction, and it
// supplies BOTH the byte numerator and the elapsed-time denominator.
const (
	// MeasurementSourceClientAppInfo means the window was built from
	// the SDK's client-origin AppInfo frames: NumBytes counted by the
	// client against ElapsedTime measured by the client's clock.
	// This is the source for both directions because it is the only
	// one that is guaranteed to be a single consistent counter/clock
	// pair on the client side.
	MeasurementSourceClientAppInfo = "ndt7_client_appinfo"
	// MeasurementSourceUnavailable means no rate could be formed;
	// see Result.WindowRejectReason.
	MeasurementSourceUnavailable = "ndt7_unavailable"
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
