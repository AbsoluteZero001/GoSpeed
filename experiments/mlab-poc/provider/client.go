package mlabpoc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-lab/locate/api/v2"
	"github.com/m-lab/ndt7-client-go"
	"github.com/m-lab/ndt7-client-go/spec"
)

const (
	defaultClientName    = "GoSpeed-p0j"
	defaultClientVersion = "0.1.0-p0j"
	defaultScheme        = "wss"
)

type Client struct {
	options Options

	mu     sync.Mutex
	cancel context.CancelFunc
	active bool
}

func NewClient(options Options) (*Client, error) {
	if options.Server != "" && options.ServiceURL != "" {
		return nil, errors.New("Server and ServiceURL are mutually exclusive")
	}
	if options.Discover != nil && (options.Server != "" || options.ServiceURL != "") {
		return nil, errors.New("Discover is mutually exclusive with Server and ServiceURL")
	}
	if options.ClientName == "" {
		options.ClientName = defaultClientName
	}
	if options.ClientVersion == "" {
		options.ClientVersion = defaultClientVersion
	}
	if options.Scheme == "" {
		options.Scheme = defaultScheme
	}
	if options.Scheme != "ws" && options.Scheme != "wss" {
		return nil, fmt.Errorf("unsupported scheme %q", options.Scheme)
	}
	if options.DownloadBudgetBytes < 0 || options.UploadBudgetBytes < 0 {
		return nil, errors.New("byte budgets must be non-negative")
	}
	return &Client{options: options}, nil
}

// Close cancels the currently active run, if any. The underlying connections
// of that run are force-closed, which aborts blocked SDK I/O immediately.
func (c *Client) Close() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Run executes a single direction. It is a thin wrapper around RunPlan.
func (c *Client) Run(ctx context.Context, direction Direction) Result {
	results := c.RunPlan(ctx, Plan{Directions: []Direction{direction}})
	return results[0]
}

// RunPlan executes the planned directions in order. Rules enforced here:
//
//   - Without an explicit, agreed ConsentRecord no connection is created at
//     all and a consent_required result is returned. This also covers the
//     Locate discovery step (P0-K-B): discovery runs only after consent.
//   - When Options.Discover is set, discovery runs first. A discovery
//     failure, cancellation or empty/invalid target list stops the plan
//     before any NDT7 download or upload starts.
//   - A non-loopback (public internet) target requires an authorized
//     RemoteGate: experimental switch enabled, target host derived from
//     validated Locate discovery, and a per-run confirmation (P0-K-C).
//     Otherwise the plan stops with remote_gate_closed and zero connections.
//   - A direction is only started when every previous direction finished
//     with StatusCompleted. After cancellation, timeout, failure, budget
//     exhaustion or an incomplete result the plan stops. There is no
//     automatic retry and no automatic restart of any direction.
//   - TotalBudgetBytes (when > 0) is a wire-level budget shared across the
//     plan; each direction receives at most the remaining budget.
func (c *Client) RunPlan(ctx context.Context, plan Plan) []Result {
	results := make([]Result, 0, len(plan.Directions))
	if len(plan.Directions) == 0 {
		return results
	}
	if !consentAgreed(c.options.Consent) {
		result := c.newResult(plan.Directions[0])
		result.Status = StatusConsentNeeded
		result.ErrorClass = ErrorConsent
		result.Error = "M-Lab public data and public IP disclosure consent is required"
		result.CompletedAt = time.Now().UTC()
		result.Quality = qualityFor(result, false)
		results = append(results, result)
		return results
	}

	// P0-K-A/B: consent-gated discovery. The SDK's built-in Locate path is
	// never used; discovery happens here, once, before any measurement.
	serviceURLs := make(map[Direction]string, len(plan.Directions))
	effectiveHost := ""
	if c.options.Discover != nil {
		targets, err := c.options.Discover(ctx)
		if err != nil {
			result := c.newResult(plan.Directions[0])
			result.Status = StatusFailed
			result.ErrorClass = classifyDiscoveryError(err)
			result.Error = err.Error()
			result.CompletedAt = time.Now().UTC()
			result.Quality = qualityFor(result, false)
			results = append(results, result)
			return results
		}
		if len(targets) == 0 {
			result := c.newResult(plan.Directions[0])
			result.Status = StatusFailed
			result.ErrorClass = ErrorNoServer
			result.Error = "discovery returned no servers"
			result.CompletedAt = time.Now().UTC()
			result.Quality = qualityFor(result, false)
			results = append(results, result)
			return results
		}
		target := targets[0]
		for _, direction := range plan.Directions {
			serviceURL, err := TargetServiceURL(&target, c.options.Scheme, direction)
			if err != nil {
				result := c.newResult(direction)
				result.Status = StatusFailed
				result.ErrorClass = ErrorServerInvalid
				result.Error = err.Error()
				result.CompletedAt = time.Now().UTC()
				result.Quality = qualityFor(result, false)
				results = append(results, result)
				return results
			}
			serviceURLs[direction] = serviceURL
		}
		host, err := hostOfServiceURL(serviceURLs[plan.Directions[0]])
		if err != nil {
			result := c.newResult(plan.Directions[0])
			result.Status = StatusFailed
			result.ErrorClass = ErrorServerInvalid
			result.Error = err.Error()
			result.CompletedAt = time.Now().UTC()
			result.Quality = qualityFor(result, false)
			results = append(results, result)
			return results
		}
		effectiveHost = host
	} else if c.options.Server != "" {
		host, _, err := net.SplitHostPort(c.options.Server)
		if err != nil {
			host = c.options.Server
		}
		effectiveHost = host
	} else if c.options.ServiceURL != "" {
		host, err := hostOfServiceURL(c.options.ServiceURL)
		if err != nil {
			result := c.newResult(plan.Directions[0])
			result.Status = StatusFailed
			result.ErrorClass = ErrorConfiguration
			result.Error = err.Error()
			result.CompletedAt = time.Now().UTC()
			result.Quality = qualityFor(result, false)
			results = append(results, result)
			return results
		}
		effectiveHost = host
	}

	// P0-K-C: the remote gate. Loopback targets (localhost mocks) are always
	// allowed; anything else requires the experimental switch, a Locate-
	// derived allow list and a per-run confirmation. A closed gate produces
	// zero network activity.
	if effectiveHost != "" && !isLoopbackHost(effectiveHost) {
		if err := c.options.RemoteGate.Authorize(effectiveHost); err != nil {
			result := c.newResult(plan.Directions[0])
			result.Status = StatusFailed
			result.ErrorClass = ErrorGateClosed
			result.Error = err.Error()
			result.CompletedAt = time.Now().UTC()
			result.Quality = qualityFor(result, false)
			results = append(results, result)
			return results
		}
	}

	var usedWireBytes int64
	for _, direction := range plan.Directions {
		directionBudget := c.directionBudget(direction)
		if plan.TotalBudgetBytes > 0 {
			remaining := plan.TotalBudgetBytes - usedWireBytes
			if remaining <= 0 {
				// The total budget is exhausted; do not start anything new.
				break
			}
			if directionBudget == 0 || directionBudget > remaining {
				directionBudget = remaining
			}
		}
		result := c.runDirection(ctx, direction, directionBudget, serviceURLs[direction])
		usedWireBytes += result.SocketBytesRead + result.SocketBytesWritten
		results = append(results, result)
		if result.Status != StatusCompleted {
			break
		}
	}
	return results
}

// classifyDiscoveryError maps Discover errors onto result error classes.
func classifyDiscoveryError(err error) ErrorClass {
	var classified *ClassifiedError
	if errors.As(err, &classified) {
		return classified.Class
	}
	return ErrorUnknown
}

// hostOfServiceURL extracts the hostname from a service URL.
func hostOfServiceURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid service URL %q: %w", raw, err)
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("service URL %q has no host", raw)
	}
	return host, nil
}

// isLoopbackHost reports whether a host is a loopback address or the literal
// name "localhost". Mock infrastructure always runs on loopback.
func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(host)
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) directionBudget(direction Direction) int64 {
	switch direction {
	case DirectionDownload:
		return c.options.DownloadBudgetBytes
	case DirectionUpload:
		return c.options.UploadBudgetBytes
	default:
		return 0
	}
}

func consentAgreed(record *ConsentRecord) bool {
	return record != nil && record.Agreed
}

func (c *Client) newResult(direction Direction) Result {
	var policyVersion string
	if c.options.Consent != nil {
		policyVersion = c.options.Consent.PolicyVersion
	}
	return Result{
		Provider:             ProviderID,
		Protocol:             ProtocolID,
		Direction:            direction,
		Status:               StatusFailed,
		ErrorClass:           ErrorUnknown,
		JitterMs:             nil,
		BudgetLimitBytes:     c.directionBudget(direction),
		PrivacyConsent:       consentAgreed(c.options.Consent),
		ConsentPolicyVersion: policyVersion,
		ClientName:           c.options.ClientName,
		ClientVersion:        c.options.ClientVersion,
		StartedAt:            time.Now().UTC(),
	}
}

func (c *Client) runDirection(ctx context.Context, direction Direction, wireBudget int64, serviceURLOverride string) Result {
	result := c.newResult(direction)
	result.BudgetLimitBytes = wireBudget
	if direction != DirectionDownload && direction != DirectionUpload {
		result.Status = StatusFailed
		result.ErrorClass = ErrorConfiguration
		result.Error = fmt.Sprintf("unsupported direction %q", direction)
		result.CompletedAt = time.Now().UTC()
		result.Quality = qualityFor(result, false)
		return result
	}

	// The wire budget is a read cap for download (bytes received from the
	// socket) and a write cap for upload (bytes sent to the socket).
	readBudget, writeBudget := int64(0), int64(0)
	switch direction {
	case DirectionDownload:
		readBudget = wireBudget
	case DirectionUpload:
		writeBudget = wireBudget
	}
	registry := newConnRegistry(readBudget, writeBudget)

	runCtx, runCancel := context.WithCancel(ctx)
	if c.options.OverallTimeout > 0 {
		runCtx, runCancel = context.WithTimeout(ctx, c.options.OverallTimeout)
	}

	// The monitor reacts to context cancellation (user cancel or overall
	// timeout) and force-closes every tracked connection. Closing the
	// underlying net.Conn wakes up SDK goroutines that are blocked in
	// NextReader / ReadMessage / WritePreparedMessage, so the run does not
	// have to wait for the 7 s SDK I/O deadline.
	var cancelObservedAt atomic.Int64
	cancelMonitorDone := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			cancelObservedAt.Store(time.Now().UnixNano())
			registry.forceCloseAll()
		case <-cancelMonitorDone:
		}
	}()

	c.mu.Lock()
	c.cancel = runCancel
	c.active = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.cancel = nil
		c.active = false
		c.mu.Unlock()
		runCancel()
		// Belt and braces: make sure no connection outlives the run even if
		// the monitor goroutine lost the select race above.
		registry.forceCloseAll()
		close(cancelMonitorDone)
	}()

	sdkClient := ndt7.NewClient(c.options.ClientName, c.options.ClientVersion)
	sdkClient.Scheme = c.options.Scheme
	// P0-K-A: GoSpeed performs its own consent-gated discovery. The SDK's
	// built-in Locate path would bypass consent and use an unconfigured
	// http.DefaultClient without timeouts, and it silently retries the next
	// Locate target on dial failures. Replace it with a refusing locator so
	// the SDK can never issue a Locate request by itself.
	sdkClient.Locate = refusingLocator{}
	if c.options.Server != "" {
		sdkClient.Server = c.options.Server
	}
	if serviceURLOverride != "" {
		serviceURL, err := url.Parse(serviceURLOverride)
		if err != nil {
			return finishConfigurationFailure(result, err)
		}
		sdkClient.ServiceURL = serviceURL
		sdkClient.Scheme = serviceURL.Scheme
	} else if c.options.ServiceURL != "" {
		serviceURL, err := url.Parse(c.options.ServiceURL)
		if err != nil {
			return finishConfigurationFailure(result, err)
		}
		sdkClient.ServiceURL = serviceURL
		sdkClient.Scheme = serviceURL.Scheme
	}

	// Inject connection management through the public websocket.Dialer field
	// of the official SDK client. The SDK version stays untouched; this is an
	// extension point the SDK explicitly documents as overridable.
	sdkClient.Dialer = websocket.Dialer{
		HandshakeTimeout: ndt7.DefaultWebSocketHandshakeTimeout,
		NetDialContext: func(dialCtx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			conn, err := dialer.DialContext(dialCtx, network, addr)
			if err != nil {
				return nil, err
			}
			tracked := registry.register(conn)
			// Re-check after registering: the run may have been cancelled or
			// budget-aborted between dial success and registration.
			if runCtx.Err() != nil {
				_ = conn.Close()
				return nil, runCtx.Err()
			}
			if registry.closed.Load() {
				_ = conn.Close()
				return nil, ErrByteBudgetExceeded
			}
			return tracked, nil
		},
	}

	var (
		events              <-chan spec.Measurement
		startErr            error
		sawFinalMeasurement bool
	)
	switch direction {
	case DirectionDownload:
		events, startErr = sdkClient.StartDownload(runCtx)
	case DirectionUpload:
		events, startErr = sdkClient.StartUpload(runCtx)
	}
	if startErr != nil {
		return finishStartFailure(result, startErr)
	}

	for event := range events {
		progress := progressFromMeasurement(result.Direction, &event)
		if progress.FinalMeasurement {
			sawFinalMeasurement = true
		}
		applyProgress(&result, progress)
		if c.options.OnProgress != nil {
			c.options.OnProgress(progress)
		}
	}
	result.CompletedAt = time.Now().UTC()
	result.ServerName = sdkClient.FQDN
	result.FinalMeasurementObserved = sawFinalMeasurement
	result.SocketBytesRead = registry.bytesRead.Load()
	result.SocketBytesWritten = registry.bytesWritten.Load()

	ctxErr := runCtx.Err()
	budgetHit := registry.budgetHit.Load()
	switch {
	case errors.Is(ctxErr, context.Canceled):
		result.Status = StatusCancelled
		result.ErrorClass = ErrorCancelled
		result.Error = ctxErr.Error()
	case errors.Is(ctxErr, context.DeadlineExceeded):
		result.Status = StatusTimeout
		result.ErrorClass = ErrorTimeout
		result.Error = ctxErr.Error()
	case budgetHit:
		result.Status = StatusBudgetExceeded
		result.ErrorClass = ErrorBudgetExceeded
		result.BudgetExceeded = true
		result.Error = "wire-level byte budget reached; the connection was force-closed"
	case result.TransferredBytes > 0 && sawFinalMeasurement:
		result.Status = StatusCompleted
		result.ErrorClass = ErrorNone
	case result.TransferredBytes > 0:
		result.Status = StatusPartial
		result.ErrorClass = ErrorServerClosed
		result.Error = "NDT7 stream ended before a final server measurement was observed"
	default:
		result.Status = StatusFailed
		result.ErrorClass = ErrorServerClosed
		result.Error = "NDT7 stream ended without application bytes"
	}

	if wireBudget > 0 {
		used := result.SocketBytesRead
		if direction == DirectionUpload {
			used = result.SocketBytesWritten
		}
		remaining := wireBudget - used
		if remaining < 0 {
			remaining = 0
		}
		result.BudgetRemainingBytes = &remaining
	}

	if result.Status == StatusTimeout || result.Status == StatusCancelled {
		if observed := cancelObservedAt.Load(); observed > 0 {
			elapsed := time.Since(time.Unix(0, observed)).Milliseconds()
			result.CancellationLatencyMs = int64Pointer(elapsed)
		}
	}
	// A budget-aborted run is never a complete NDT7 test, even when a final
	// server measurement happened to be observed before the stop.
	final := sawFinalMeasurement && result.Status == StatusCompleted
	result.Quality = qualityFor(result, final)
	return result
}

func finishConfigurationFailure(result Result, err error) Result {
	result.Status = StatusFailed
	result.ErrorClass = ErrorConfiguration
	result.Error = err.Error()
	result.CompletedAt = time.Now().UTC()
	result.Quality = qualityFor(result, false)
	return result
}

func finishStartFailure(result Result, err error) Result {
	result.Status = StatusFailed
	result.ErrorClass = classifyStartError(err)
	result.Error = err.Error()
	result.CompletedAt = time.Now().UTC()
	result.Quality = qualityFor(result, false)
	return result
}

// refusingLocator implements the SDK's ndt7.Locator interface by always
// failing. It is injected into every SDK client instance so the SDK can never
// perform its own Locate HTTP request: discovery is owned by GoSpeed, behind
// the consent gate and validation.
type refusingLocator struct{}

func (refusingLocator) Nearest(context.Context, string) ([]v2.Target, error) {
	return nil, errors.New("SDK-initiated Locate is disabled; GoSpeed performs consent-gated discovery itself")
}

func classifyStartError(err error) ErrorClass {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCancelled
	}
	if errors.Is(err, ErrByteBudgetExceeded) {
		return ErrorBudgetExceeded
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "websocket"), strings.Contains(message, "bad handshake"),
		strings.Contains(message, "upgrade"):
		return ErrorHandshake
	case strings.Contains(message, "lookup"), strings.Contains(message, "dial"),
		strings.Contains(message, "connection refused"), strings.Contains(message, "network"):
		return ErrorNetwork
	default:
		return ErrorUnknown
	}
}

func qualityFor(result Result, final bool) Quality {
	notes := make([]string, 0, 3)
	completeness := "none"
	if result.Status == StatusCompleted {
		completeness = "complete"
	} else if result.TransferredBytes > 0 || result.SocketBytesRead > 0 || result.SocketBytesWritten > 0 {
		completeness = "partial"
	}
	stability := "unknown"
	trust := "insufficient"
	if completeness == "complete" && final {
		trust = "normal"
	}
	switch {
	case result.Status == StatusBudgetExceeded:
		notes = append(notes, "The run was stopped by the experimental wire-level byte budget; it is not a complete NDT7 test.")
	case completeness == "complete":
		notes = append(notes, "A final server-side NDT7 measurement was observed.")
	case completeness == "partial":
		notes = append(notes, "The NDT7 stream ended before a complete final measurement.")
	default:
		notes = append(notes, "No usable NDT7 application measurement completed.")
	}
	if result.JitterMs == nil {
		notes = append(notes, "Jitter is N/A because NDT7 does not natively provide application-layer jitter.")
	}
	return Quality{
		Completeness: completeness,
		Stability:    stability,
		Trust:        trust,
		Notes:        notes,
	}
}

func progressFromMeasurement(direction Direction, measurement *spec.Measurement) Progress {
	progress := Progress{
		Direction: direction,
		Origin:    string(measurement.Origin),
		Test:      string(measurement.Test),
	}
	if measurement.AppInfo != nil {
		progress.ApplicationBytes = measurement.AppInfo.NumBytes
		progress.ElapsedMs = measurement.AppInfo.ElapsedTime / 1000
	}
	if measurement.TCPInfo != nil {
		if measurement.TCPInfo.BytesSent > 0 {
			value := measurement.TCPInfo.BytesSent
			progress.TCPBytesSent = &value
		}
		if measurement.TCPInfo.BytesReceived > 0 {
			value := measurement.TCPInfo.BytesReceived
			progress.TCPBytesReceived = &value
		}
		if measurement.TCPInfo.MinRTT > 0 {
			value := float64(measurement.TCPInfo.MinRTT) / 1000
			progress.TCPMinRTTMs = &value
		}
		if measurement.TCPInfo.RTTVar > 0 {
			value := float64(measurement.TCPInfo.RTTVar) / 1000
			progress.TCPRTTVarMs = &value
		}
		progress.FinalMeasurement = measurement.TCPInfo.MinRTT > 0
	}
	return progress
}

func applyProgress(result *Result, progress Progress) {
	if progress.ApplicationBytes > result.TransferredBytes {
		result.TransferredBytes = progress.ApplicationBytes
	}
	if progress.ElapsedMs > result.MeasurementDurationMs {
		result.MeasurementDurationMs = progress.ElapsedMs
	}
	if progress.TCPBytesSent != nil && *progress.TCPBytesSent > result.TCPBytesSent {
		result.TCPBytesSent = *progress.TCPBytesSent
	}
	if progress.TCPBytesReceived != nil && *progress.TCPBytesReceived > result.TCPBytesReceived {
		result.TCPBytesReceived = *progress.TCPBytesReceived
	}
	if progress.TCPMinRTTMs != nil &&
		(result.TCPMinRTTMs == nil || *progress.TCPMinRTTMs < *result.TCPMinRTTMs) {
		result.TCPMinRTTMs = float64Pointer(*progress.TCPMinRTTMs)
	}
	if progress.TCPRTTVarMs != nil {
		result.TCPRTTVarMs = float64Pointer(*progress.TCPRTTVarMs)
	}
	switch result.Direction {
	case DirectionDownload:
		if progress.ApplicationBytes <= 0 || progress.ElapsedMs <= 0 {
			return
		}
		mbps := float64(progress.ApplicationBytes) * 8 * 1_000_000 /
			float64(progress.ElapsedMs) / 1_000_000
		result.DownloadGoodputMbps = float64Pointer(mbps)
	case DirectionUpload:
		if progress.Origin == "server" && progress.TCPBytesReceived != nil &&
			progress.ElapsedMs > 0 {
			mbps := float64(*progress.TCPBytesReceived) * 8 * 1_000_000 /
				float64(progress.ElapsedMs) / 1_000_000
			result.UploadGoodputMbps = float64Pointer(mbps)
			return
		}
		if progress.ApplicationBytes <= 0 || progress.ElapsedMs <= 0 {
			return
		}
		mbps := float64(progress.ApplicationBytes) * 8 * 1_000_000 /
			float64(progress.ElapsedMs) / 1_000_000
		result.UploadGoodputMbps = float64Pointer(mbps)
	}
}
