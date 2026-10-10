package mlabpoc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/m-lab/ndt7-client-go"
	"github.com/m-lab/ndt7-client-go/spec"
)

const (
	defaultClientName    = "GoSpeed-p0i"
	defaultClientVersion = "0.1.0-p0i"
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
	if options.Server == "" && options.ServiceURL == "" {
		// An empty Server/ServiceURL means the official Locate v2 default.
		// The caller still has to explicitly opt in to a real run.
	}
	return &Client{options: options}, nil
}

func (c *Client) Close() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Client) Run(ctx context.Context, direction Direction) Result {
	result := Result{
		Provider:         ProviderID,
		Protocol:         ProtocolID,
		Direction:        direction,
		Status:           StatusFailed,
		ErrorClass:       ErrorUnknown,
		JitterMs:         nil,
		BudgetLimitBytes: c.options.SoftByteBudget,
		PrivacyConsent:   c.options.PrivacyConsent,
		ClientName:       c.options.ClientName,
		ClientVersion:    c.options.ClientVersion,
		StartedAt:        time.Now().UTC(),
	}
	if !c.options.PrivacyConsent {
		result.Status = StatusConsentNeeded
		result.ErrorClass = ErrorConsent
		result.Error = "M-Lab public data and public IP disclosure consent is required"
		result.CompletedAt = time.Now().UTC()
		result.Quality = qualityFor(result, false)
		return result
	}
	if direction != DirectionDownload && direction != DirectionUpload {
		result.Status = StatusFailed
		result.ErrorClass = ErrorConfiguration
		result.Error = fmt.Sprintf("unsupported direction %q", direction)
		result.CompletedAt = time.Now().UTC()
		result.Quality = qualityFor(result, false)
		return result
	}

	runCtx, runCancel := context.WithCancel(ctx)
	if c.options.OverallTimeout > 0 {
		runCtx, runCancel = context.WithTimeout(ctx, c.options.OverallTimeout)
	}
	var cancelObservedAt atomic.Int64
	cancelMonitorDone := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			cancelObservedAt.Store(time.Now().UnixNano())
		case <-cancelMonitorDone:
		}
	}()
	defer close(cancelMonitorDone)
	c.mu.Lock()
	c.cancel = runCancel
	c.active = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.cancel != nil {
			c.cancel = nil
		}
		c.active = false
		c.mu.Unlock()
		runCancel()
	}()

	sdkClient := ndt7.NewClient(c.options.ClientName, c.options.ClientVersion)
	sdkClient.Scheme = c.options.Scheme
	if c.options.Server != "" {
		sdkClient.Server = c.options.Server
	}
	if c.options.ServiceURL != "" {
		serviceURL, err := url.Parse(c.options.ServiceURL)
		if err != nil {
			return finishConfigurationFailure(result, err)
		}
		sdkClient.ServiceURL = serviceURL
		sdkClient.Scheme = serviceURL.Scheme
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

	budgetTriggered := false
	for event := range events {
		progress := progressFromMeasurement(result.Direction, &event)
		if progress.FinalMeasurement {
			sawFinalMeasurement = true
		}
		applyProgress(&result, progress)
		if c.options.OnProgress != nil {
			c.options.OnProgress(progress)
		}
		if !budgetTriggered && c.options.SoftByteBudget > 0 &&
			result.TransferredBytes >= c.options.SoftByteBudget {
			budgetTriggered = true
			result.BudgetExceeded = true
			runCancel()
		}
	}
	result.CompletedAt = time.Now().UTC()
	result.ServerName = sdkClient.FQDN
	result.FinalMeasurementObserved = sawFinalMeasurement

	ctxErr := runCtx.Err()
	switch {
	case budgetTriggered:
		result.Status = StatusBudgetExceeded
		result.ErrorClass = ErrorBudgetExceeded
		result.Error = "soft byte budget exceeded; in-flight traffic may continue until the SDK I/O path closes"
	case errors.Is(ctxErr, context.DeadlineExceeded):
		result.Status = StatusTimeout
		result.ErrorClass = ErrorTimeout
		result.Error = ctxErr.Error()
	case errors.Is(ctxErr, context.Canceled):
		result.Status = StatusCancelled
		result.ErrorClass = ErrorCancelled
		result.Error = ctxErr.Error()
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
	if result.Status == StatusTimeout || result.Status == StatusCancelled {
		if observed := cancelObservedAt.Load(); observed > 0 {
			elapsed := time.Since(time.Unix(0, observed)).Milliseconds()
			result.CancellationLatencyMs = int64Pointer(elapsed)
		}
	}
	result.Quality = qualityFor(result, result.FinalMeasurementObserved)
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

func classifyStartError(err error) ErrorClass {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrorTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ErrorCancelled
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
	notes := make([]string, 0, 2)
	completeness := "none"
	if result.Status == StatusCompleted {
		completeness = "complete"
	} else if result.TransferredBytes > 0 {
		completeness = "partial"
	}
	stability := "unknown"
	if result.TransferredBytes > 0 {
		stability = "unknown"
	}
	trust := "insufficient"
	if completeness == "complete" && final {
		trust = "normal"
	}
	if completeness == "complete" {
		notes = append(notes, "A final server-side NDT7 measurement was observed.")
	} else if completeness == "partial" {
		notes = append(notes, "The NDT7 stream ended before a complete final measurement.")
	} else {
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
