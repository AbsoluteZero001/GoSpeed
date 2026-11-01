package main

import (
	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// Event names pushed to the frontend. The GUI subscribes to these once and
// never polls the backend for measurement state.
const (
	// EventState reports runner level transitions (idle, probing, cancelling).
	EventState = "gospeed:state"
	// EventTarget reports the node a run was resolved to, including the
	// measured reason when the node was selected automatically.
	EventTarget = "gospeed:target"
	// EventProgress carries real time samples from the measurement engine.
	EventProgress = "gospeed:progress"
	// EventFinished carries the final result or the exact failure.
	EventFinished = "gospeed:finished"
	// EventNodes carries the outcome of a node health check.
	EventNodes = "gospeed:nodes"
)

// Runner states. The running states map one to one onto speedtest.RunState so
// the UI never has to reinterpret phase names.
const (
	StateIdle            = "idle"
	StateChecking        = "checking"
	StateProbing         = "probing"
	StateCancelling      = "cancelling"
	StatePreparing       = string(speedtest.StatePreparing)
	StateLatencyTesting  = string(speedtest.StateLatencyTesting)
	StateDownloadTesting = string(speedtest.StateDownloadTesting)
	StateUploadTesting   = string(speedtest.StateUploadTesting)
	StateCompleted       = string(speedtest.StateCompleted)
	StateFailed          = string(speedtest.StateFailed)
	StateCancelled       = string(speedtest.StateCancelled)
)

// Selection modes accepted by StartTest.
const (
	SelectionModeAuto   = "auto"
	SelectionModeManual = "manual"
)

// StateEvent is the payload of EventState.
type StateEvent struct {
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// AppInfo describes the build and the effective test defaults.
type AppInfo struct {
	Version                 string   `json:"version"`
	GoVersion               string   `json:"goVersion"`
	Platform                string   `json:"platform"`
	MaxConnections          int      `json:"maxConnections"`
	DefaultConnections      int      `json:"defaultConnections"`
	DefaultDurationMs       int      `json:"defaultDurationMs"`
	DefaultSampleIntervalMs int      `json:"defaultSampleIntervalMs"`
	MinDurationMs           int      `json:"minDurationMs"`
	MaxDurationMs           int      `json:"maxDurationMs"`
	MinSampleIntervalMs     int      `json:"minSampleIntervalMs"`
	MaxSampleIntervalMs     int      `json:"maxSampleIntervalMs"`
	LatencySamples          int      `json:"latencySamples"`
	NodeConfigPath          string   `json:"nodeConfigPath"`
	NodeConfigSource        string   `json:"nodeConfigSource"`
	ConfigSearchPaths       []string `json:"configSearchPaths"`
	NodeCount               int      `json:"nodeCount"`
}

// NodeView is one configured node as shown in the node picker.
type NodeView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	BaseURL      string `json:"baseUrl"`
	Protocol     string `json:"protocol"`
	Enabled      bool   `json:"enabled"`
	Local        bool   `json:"local"`
	Provider     string `json:"provider,omitempty"`
	Description  string `json:"description,omitempty"`
	Location     string `json:"location"`
	NetworkScope string `json:"networkScope"`
}

// NodeListResult is returned by ListNodes.
type NodeListResult struct {
	Path   string     `json:"path"`
	Source string     `json:"source"`
	Nodes  []NodeView `json:"nodes"`
	// Notice is set when the configured nodes cannot measure anything beyond
	// this machine (only loopback nodes, or no enabled node at all).
	Notice *MeasurementNotice `json:"notice,omitempty"`
}

// NodeStatusView is one node together with the staged timings of its last
// health check. Missing values stay null; they are never filled with zero.
type NodeStatusView struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	BaseURL                string   `json:"baseUrl"`
	Protocol               string   `json:"protocol"`
	Enabled                bool     `json:"enabled"`
	Local                  bool     `json:"local"`
	NetworkScope           string   `json:"networkScope"`
	Status                 string   `json:"status"`
	HTTPRTTMs              *float64 `json:"httpRttMs,omitempty"`
	DNSMs                  *float64 `json:"dnsMs,omitempty"`
	TCPMs                  *float64 `json:"tcpMs,omitempty"`
	TLSMs                  *float64 `json:"tlsMs,omitempty"`
	LatencyMedianMs        *float64 `json:"latencyMedianMs,omitempty"`
	LatencySamples         int      `json:"latencySamples"`
	LatencyBelowResolution int      `json:"latencyBelowResolution"`
	LatencyFailures        int      `json:"latencyFailures"`
	Attempts               int      `json:"attempts"`
	ServerVersion          string   `json:"serverVersion,omitempty"`
	Capabilities           bool     `json:"capabilities"`
	CapabilitiesLegacy     bool     `json:"capabilitiesLegacy"`
	Detail                 string   `json:"detail,omitempty"`
	CheckedAt              string   `json:"checkedAt,omitempty"`
}

// NodeCheckResult is returned by CheckNodes.
type NodeCheckResult struct {
	Path      string           `json:"path"`
	CheckedAt string           `json:"checkedAt"`
	Cancelled bool             `json:"cancelled"`
	Nodes     []NodeStatusView `json:"nodes"`
}

// NodeTargetView identifies the node a test was resolved to.
type NodeTargetView struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	BaseURL         string   `json:"baseUrl"`
	Protocol        string   `json:"protocol"`
	Local           bool     `json:"local"`
	NetworkScope    string   `json:"networkScope"`
	HealthStatus    string   `json:"healthStatus,omitempty"`
	HealthLatencyMs *float64 `json:"healthLatencyMs,omitempty"`
}

// CandidateView is one ranked candidate of an automatic node selection.
type CandidateView struct {
	Rank      int      `json:"rank"`
	NodeID    string   `json:"nodeId"`
	NodeName  string   `json:"nodeName"`
	Status    string   `json:"status"`
	LatencyMs *float64 `json:"latencyMs,omitempty"`
	Reason    string   `json:"reason"`
}

// TargetEvent is the payload of EventTarget.
type TargetEvent struct {
	Target          NodeTargetView  `json:"target"`
	SelectionMethod string          `json:"selectionMethod"`
	SelectionReason string          `json:"selectionReason,omitempty"`
	SelectionNote   string          `json:"selectionNote,omitempty"`
	Candidates      []CandidateView `json:"candidates,omitempty"`
	ConfigPath      string          `json:"configPath,omitempty"`
	// Notice describes what the target address means for the numbers, for
	// example that a loopback run is local throughput only.
	Notice *MeasurementNotice `json:"notice,omitempty"`
}

// ProgressEvent is the payload of EventProgress. Every value is copied from
// the engine's measured counters; nothing is interpolated or synthesized.
type ProgressEvent struct {
	State             string   `json:"state"`
	Phase             string   `json:"phase,omitempty"`
	Stage             string   `json:"stage"`
	ElapsedMs         float64  `json:"elapsedMs"`
	Bytes             int64    `json:"bytes"`
	Mbps              float64  `json:"mbps"`
	InstantMbps       float64  `json:"instantMbps"`
	ActiveConnections int      `json:"activeConnections"`
	Samples           int      `json:"samples"`
	BudgetFraction    *float64 `json:"budgetFraction,omitempty"`
	BudgetRemainingMs *float64 `json:"budgetRemainingMs,omitempty"`
	Message           string   `json:"message,omitempty"`
}

// FinishedEvent is the payload of EventFinished. A failed or cancelled run
// carries the exact engine error; it never carries a made up rate.
type FinishedEvent struct {
	Status     string  `json:"status"`
	TestID     string  `json:"testId,omitempty"`
	Error      string  `json:"error,omitempty"`
	Cancelled  bool    `json:"cancelled"`
	DurationMs float64 `json:"durationMs"`
	// Result is the provider's own native result. It is optional: a provider
	// that has no native detail reports nil and the UI falls back to Unified.
	Result *speedtest.Result `json:"result,omitempty"`
	// Unified is the provider independent verdict for the run: which provider
	// served it, how reachable the node really was, what each metric is and
	// how it was measured, and how complete and trustworthy the evidence is.
	//
	// It is present for every run, including cancelled and failed ones, because
	// the status distinction (completed, failed, cancelled, timeout,
	// budget_exceeded) is exactly what the UI has to render correctly.
	Unified *provider.FinalResult `json:"unified,omitempty"`
	// Notice repeats the target notice so the result panel stays accurate even
	// when it renders without the target event.
	Notice *MeasurementNotice `json:"notice,omitempty"`
}

// StartOptions is the user request accepted by StartTest.
type StartOptions struct {
	// SelectionMode is "auto" (probe and rank enabled nodes) or "manual".
	SelectionMode string `json:"selectionMode"`
	// NodeID names the node for manual selection. It is ignored for "auto".
	NodeID string `json:"nodeId"`
	// Connections is the number of parallel connections per transfer phase.
	Connections int `json:"connections"`
	// DurationMs is the transfer window of each phase in milliseconds.
	DurationMs int `json:"durationMs"`
	// SampleIntervalMs is the real time sampling cadence in milliseconds.
	SampleIntervalMs int `json:"sampleIntervalMs"`

	// Provider selects the measurement backend. An empty value means the
	// default GoSpeed HTTP provider, so a request that predates this field
	// keeps its meaning.
	Provider string `json:"provider,omitempty"`
	// ProviderNodeRef names a node a provider discovered. It is an opaque
	// reference the backend minted; the frontend never holds a service URL
	// and cannot send one, because there is no field for it.
	ProviderNodeRef string `json:"providerNodeRef,omitempty"`
	// Direction selects the transfer direction for a provider that measures
	// one direction per run. It is "download" or "upload".
	Direction string `json:"direction,omitempty"`
}

// NDT7ConsentState is what the GUI needs to render the M-Lab panel: whether
// a current consent exists, the full disclosure text, and the state of
// today's counter.
//
// It carries no credential, no endpoint and no node address.
type NDT7ConsentState struct {
	// Provider is the provider these settings belong to.
	Provider string `json:"provider"`
	// ConsentRequired is true while no consent of the current wording
	// version is on record.
	ConsentRequired bool `json:"consentRequired"`
	// Agreed reports a consent whose wording version is the current one.
	Agreed bool `json:"agreed"`
	// PolicyVersion is the wording version in effect. A stored agreement
	// under an older version does not count.
	PolicyVersion string `json:"policyVersion"`
	// Notice is the full disclosure text. The GUI shows it verbatim: the
	// backend holds the canonical wording, so the GUI cannot shorten it.
	Notice string `json:"notice"`
	// DailyLimit, DailyUsed and DailyRemaining describe today's budget.
	DailyLimit     int `json:"dailyLimit"`
	DailyUsed      int `json:"dailyUsed"`
	DailyRemaining int `json:"dailyRemaining"`
	// DailyAvailable reports whether the counter could be read. When it is
	// false the provider refuses to measure, because a limit that cannot be
	// enforced is not a limit.
	DailyAvailable bool `json:"dailyAvailable"`
	// DailyError explains why the counter is unavailable.
	DailyError string `json:"dailyError,omitempty"`
	// Budget is the traffic budget the provider applies to one run.
	Budget NDT7BudgetView `json:"budget"`
	// NodeCount is how many discovered nodes the backend currently holds.
	// The list is dropped when a session ends, because the access token it
	// carries is short lived.
	NodeCount int `json:"nodeCount"`
}

// NDT7BudgetView is the provider's traffic budget for one run.
type NDT7BudgetView struct {
	DownloadBytes int64 `json:"downloadBytes"`
	UploadBytes   int64 `json:"uploadBytes"`
	TotalBytes    int64 `json:"totalBytes"`
}

// NDT7NodeListResult is the result of a node discovery request.
type NDT7NodeListResult struct {
	// Nodes are the discovered nodes as GUI safe views. They carry no
	// endpoint and no access token.
	Nodes []ProviderNodeView `json:"nodes"`
	// Consent is the current state of the gates, so the GUI can render the
	// counter and the disclosure next to the list.
	Consent NDT7ConsentState `json:"consent"`
}

// NDT7RunAuthorization acknowledges one single use run authorization.
//
// It is evidence that the backend issued a grant for one node and one
// direction. It carries no credential and no address beyond the node's
// hostname, which is public information.
type NDT7RunAuthorization struct {
	Granted bool `json:"granted"`
	// NodeID is the node the grant is bound to.
	NodeID string `json:"nodeId"`
	// Direction is the direction the grant is bound to.
	Direction string `json:"direction"`
	// Seq correlates the grant with the log.
	Seq int `json:"seq"`
}

// StartTestResult acknowledges an accepted run. Progress is reported through
// events, not through this value.
type StartTestResult struct {
	Started bool   `json:"started"`
	Mode    string `json:"mode"`
	Message string `json:"message,omitempty"`
}

// StatusResult is the one shot state snapshot used when the UI mounts.
type StatusResult struct {
	Busy    bool   `json:"busy"`
	Kind    string `json:"kind,omitempty"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}
