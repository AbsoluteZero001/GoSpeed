package mlabpoc

import (
	"context"
	"time"

	v2 "github.com/m-lab/locate/api/v2"
)

const (
	ProviderID = "mlab-ndt7"
	ProtocolID = "ndt7"
)

// Experimental PoC defaults for the planned traffic budget and the overall
// timeout. They mirror the numbers announced in the consent notice. They are
// wire-level (socket layer) experimental protection values, not ISP billing
// limits, and they are not part of the Goodput formula.
const (
	DefaultDownloadBudgetBytes int64         = 24 << 20 // 24 MiB
	DefaultUploadBudgetBytes   int64         = 16 << 20 // 16 MiB
	DefaultTotalBudgetBytes    int64         = 40 << 20 // 40 MiB
	DefaultOverallTimeout      time.Duration = 30 * time.Second
	DefaultLocateTimeout       time.Duration = 10 * time.Second
)

type Direction string

const (
	DirectionDownload Direction = "download"
	DirectionUpload   Direction = "upload"
)

type Status string

const (
	StatusCompleted      Status = "completed"
	StatusPartial        Status = "partial"
	StatusCancelled      Status = "cancelled"
	StatusTimeout        Status = "timeout"
	StatusFailed         Status = "failed"
	StatusConsentNeeded  Status = "consent_required"
	StatusBudgetExceeded Status = "budget_exceeded"
)

type ErrorClass string

const (
	ErrorNone           ErrorClass = "none"
	ErrorConsent        ErrorClass = "consent_required"
	ErrorConfiguration  ErrorClass = "configuration"
	ErrorHandshake      ErrorClass = "handshake"
	ErrorNetwork        ErrorClass = "network"
	ErrorTimeout        ErrorClass = "timeout"
	ErrorCancelled      ErrorClass = "cancelled"
	ErrorServerClosed   ErrorClass = "server_closed"
	ErrorBudgetExceeded ErrorClass = "budget_exceeded"
	ErrorNoServer       ErrorClass = "no_server"
	ErrorServerInvalid  ErrorClass = "server_invalid"
	ErrorGateClosed     ErrorClass = "remote_gate_closed"
	ErrorUnknown        ErrorClass = "unknown"
)

// ClassifiedError carries an ErrorClass alongside an arbitrary error so the
// plan runner can map discovery and gate failures onto result records.
type ClassifiedError struct {
	Class ErrorClass
	Err   error
}

func (e *ClassifiedError) Error() string { return e.Err.Error() }
func (e *ClassifiedError) Unwrap() error { return e.Err }

func newClassifiedError(class ErrorClass, err error) *ClassifiedError {
	return &ClassifiedError{Class: class, Err: err}
}

type Quality struct {
	Completeness string   `json:"completeness"`
	Stability    string   `json:"stability"`
	Trust        string   `json:"trust"`
	Notes        []string `json:"notes"`
}

type Progress struct {
	Direction        Direction `json:"direction"`
	Origin           string    `json:"origin"`
	Test             string    `json:"test"`
	ElapsedMs        int64     `json:"elapsedMs"`
	ApplicationBytes int64     `json:"applicationBytes"`
	TCPBytesSent     *int64    `json:"tcpBytesSent"`
	TCPBytesReceived *int64    `json:"tcpBytesReceived"`
	TCPMinRTTMs      *float64  `json:"tcpMinRTTMs"`
	TCPRTTVarMs      *float64  `json:"tcpRTTVarMs"`
	FinalMeasurement bool      `json:"finalMeasurement"`
}

type Result struct {
	Provider   string     `json:"provider"`
	Protocol   string     `json:"protocol"`
	Direction  Direction  `json:"direction"`
	Status     Status     `json:"status"`
	ErrorClass ErrorClass `json:"errorClass"`
	Error      string     `json:"error,omitempty"`
	// DownloadGoodputMbps / UploadGoodputMbps are true Mbit/s values:
	// applicationBytes × 8 ÷ (elapsedMs × 1000). P0-L corrected the unit;
	// earlier releases computed kbit/s under the same field name.
	DownloadGoodputMbps      *float64  `json:"downloadGoodputMbps"`
	UploadGoodputMbps        *float64  `json:"uploadGoodputMbps"`
	TCPMinRTTMs              *float64  `json:"tcpMinRTTMs"`
	TCPRTTVarMs              *float64  `json:"tcpRTTVarMs"`
	JitterMs                 *float64  `json:"jitterMs"`
	ServerName               string    `json:"serverName"`
	MeasurementDurationMs    int64     `json:"measurementDurationMs"`
	TransferredBytes         int64     `json:"transferredBytes"`
	TCPBytesSent             int64     `json:"tcpBytesSent"`
	TCPBytesReceived         int64     `json:"tcpBytesReceived"`
	BudgetLimitBytes         int64     `json:"budgetLimitBytes"`
	BudgetExceeded           bool      `json:"budgetExceeded"`
	BudgetRemainingBytes     *int64    `json:"budgetRemainingBytes,omitempty"`
	SocketBytesRead          int64     `json:"socketBytesRead"`
	SocketBytesWritten       int64     `json:"socketBytesWritten"`
	CancellationLatencyMs    *int64    `json:"cancellationLatencyMs"`
	PrivacyConsent           bool      `json:"privacyConsent"`
	ConsentPolicyVersion     string    `json:"consentPolicyVersion,omitempty"`
	ClientName               string    `json:"clientName"`
	ClientVersion            string    `json:"clientVersion"`
	FinalMeasurementObserved bool      `json:"finalMeasurementObserved"`
	Quality                  Quality   `json:"quality"`
	StartedAt                time.Time `json:"startedAt"`
	CompletedAt              time.Time `json:"completedAt"`
}

// Options configures one Client. Byte budgets are enforced at the wire layer
// (socket bytes), which bounds the application payload from above because
// framing overhead is always non-negative. They are experimental PoC values,
// not ISP billing limits.
//
// A Client is intended for a single measurement plan. The Discover function
// and the RemoteGate carry per-run state (a discovery call and a per-run
// confirmation), so construct a fresh Client for every run instead of reusing
// one.
type Options struct {
	ClientName          string
	ClientVersion       string
	Server              string
	ServiceURL          string
	Scheme              string
	OverallTimeout      time.Duration
	DownloadBudgetBytes int64 // wire-level read cap for download, 0 = disabled
	UploadBudgetBytes   int64 // wire-level write cap for upload, 0 = disabled
	Consent             *ConsentRecord
	OnProgress          func(Progress)

	// Discover, when set, performs consent-gated server discovery (the
	// M-Lab Locate v2 query) before any direction runs. It must return
	// pre-validated targets. It is mutually exclusive with Server and
	// ServiceURL.
	Discover func(ctx context.Context) ([]v2.Target, error)

	// RemoteGate guards non-loopback (public internet) targets. A loopback
	// target never consults the gate. See provider/remote.go.
	RemoteGate *RemoteGate
}

// Plan describes a sequence of directions to run in order. A direction is
// only started when every previous direction finished with StatusCompleted.
// TotalBudgetBytes is a wire-level budget shared across the whole plan,
// 0 = unlimited.
type Plan struct {
	Directions       []Direction
	TotalBudgetBytes int64
}

func float64Pointer(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}
