package mlabpoc

import "time"

const (
	ProviderID = "mlab-ndt7"
	ProtocolID = "ndt7"
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
	ErrorUnknown        ErrorClass = "unknown"
)

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
	Provider                 string     `json:"provider"`
	Protocol                 string     `json:"protocol"`
	Direction                Direction  `json:"direction"`
	Status                   Status     `json:"status"`
	ErrorClass               ErrorClass `json:"errorClass"`
	Error                    string     `json:"error,omitempty"`
	DownloadGoodputMbps      *float64   `json:"downloadGoodputMbps"`
	UploadGoodputMbps        *float64   `json:"uploadGoodputMbps"`
	TCPMinRTTMs              *float64   `json:"tcpMinRTTMs"`
	TCPRTTVarMs              *float64   `json:"tcpRTTVarMs"`
	JitterMs                 *float64   `json:"jitterMs"`
	ServerName               string     `json:"serverName"`
	MeasurementDurationMs    int64      `json:"measurementDurationMs"`
	TransferredBytes         int64      `json:"transferredBytes"`
	TCPBytesSent             int64      `json:"tcpBytesSent"`
	TCPBytesReceived         int64      `json:"tcpBytesReceived"`
	BudgetLimitBytes         int64      `json:"budgetLimitBytes"`
	BudgetExceeded           bool       `json:"budgetExceeded"`
	BudgetRemainingBytes     *int64     `json:"budgetRemainingBytes,omitempty"`
	SocketBytesRead          int64      `json:"socketBytesRead"`
	SocketBytesWritten       int64      `json:"socketBytesWritten"`
	CancellationLatencyMs    *int64     `json:"cancellationLatencyMs"`
	PrivacyConsent           bool       `json:"privacyConsent"`
	ConsentPolicyVersion     string     `json:"consentPolicyVersion,omitempty"`
	ClientName               string     `json:"clientName"`
	ClientVersion            string     `json:"clientVersion"`
	FinalMeasurementObserved bool       `json:"finalMeasurementObserved"`
	Quality                  Quality    `json:"quality"`
	StartedAt                time.Time  `json:"startedAt"`
	CompletedAt              time.Time  `json:"completedAt"`
}

// Options configures one Client. Byte budgets are enforced at the wire layer
// (socket bytes), which bounds the application payload from above because
// framing overhead is always non-negative. They are experimental PoC values,
// not ISP billing limits.
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
