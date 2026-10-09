package speedtest

import (
	"errors"
	"fmt"
	"time"
)

// ErrMixedConditions reports that repeated runs do not share the same
// execution conditions. Statistics must never mix different connection counts,
// durations or targets into one population.
var ErrMixedConditions = errors.New("speedtest: runs were executed under different conditions")

// MetricStats is the descriptive statistics of one metric across runs.
// Optional values are null when the runs cannot support them.
type MetricStats struct {
	Samples    int      `json:"samples"`
	Mean       *float64 `json:"mean"`
	Median     *float64 `json:"median"`
	Min        *float64 `json:"min"`
	Max        *float64 `json:"max"`
	StdDev     *float64 `json:"stddev"`
	CVPercent  *float64 `json:"cv_percent"`
	StdDevKind string   `json:"stddev_kind,omitempty"`
}

// RunSummary is the compact per-run evidence kept inside a Summary. Every run
// stays visible, including failed and cancelled ones, so an aggregate can
// always be traced back to its inputs.
type RunSummary struct {
	Index        int       `json:"index"`
	TestID       string    `json:"test_id"`
	Timestamp    time.Time `json:"timestamp"`
	Status       Status    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`

	DownloadMbps       *float64       `json:"download_mbps,omitempty"`
	DownloadBytes      *int64         `json:"download_bytes,omitempty"`
	DownloadDurationNs *time.Duration `json:"download_duration_ns,omitempty"`
	UploadMbps         *float64       `json:"upload_mbps,omitempty"`
	UploadBytes        *int64         `json:"upload_bytes,omitempty"`
	UploadDurationNs   *time.Duration `json:"upload_duration_ns,omitempty"`
	LatencyAverageNs   *time.Duration `json:"latency_average_ns,omitempty"`
	JitterNs           *time.Duration `json:"jitter_ns,omitempty"`

	Connections int      `json:"connections"`
	Warnings    []string `json:"warnings,omitempty"`
}

// Summary aggregates repeated runs of the same test. Only completed runs feed
// the statistics; failed and cancelled runs are counted and still listed.
type Summary struct {
	GeneratedAt time.Time `json:"generated_at"`
	NodeID      string    `json:"node_id"`
	NodeName    string    `json:"node_name"`
	// ServerAddress and Protocol describe what was measured.
	ServerAddress string   `json:"server_address"`
	Protocol      Protocol `json:"protocol"`
	NetworkScope  string   `json:"network_scope,omitempty"`

	// Connections, RequestedDurationNs and MaxBytes are the shared conditions
	// every run used.
	Connections         int           `json:"connections"`
	RequestedDurationNs time.Duration `json:"requested_duration_ns"`
	MaxBytes            int64         `json:"max_bytes"`

	Runs      int `json:"runs"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`

	DownloadMbps MetricStats `json:"download_mbps"`
	UploadMbps   MetricStats `json:"upload_mbps"`
	LatencyMs    MetricStats `json:"latency_ms"`
	JitterMs     MetricStats `json:"jitter_ms"`

	RunDetails []RunSummary `json:"run_details"`
	Warnings   []string     `json:"warnings,omitempty"`
}

// BatchResult is the JSON document produced for a repeated test. A single run
// keeps the plain Result shape for v0.2.0 compatibility; only --repeat > 1
// uses this wrapper.
type BatchResult struct {
	Summary Summary  `json:"summary"`
	Results []Result `json:"results"`
}

// SummarizeRuns aggregates repeated runs. It refuses to mix different targets,
// connection counts, durations or byte budgets, because such a population
// would not describe one measurable condition.
func SummarizeRuns(results []Result) (Summary, error) {
	if len(results) == 0 {
		return Summary{}, errors.New("speedtest: no results to summarize")
	}
	first := results[0]
	summary := Summary{
		GeneratedAt:         time.Now().UTC(),
		NodeID:              first.Target.ServerID,
		NodeName:            first.Target.ServerName,
		ServerAddress:       first.Target.ServerAddress,
		Protocol:            first.Target.Protocol,
		NetworkScope:        string(first.Target.NetworkScope),
		Connections:         first.Settings.Connections,
		RequestedDurationNs: first.Settings.DurationNs,
		MaxBytes:            first.Settings.MaxBytes,
		Runs:                len(results),
	}
	downloadValues := make([]float64, 0, len(results))
	uploadValues := make([]float64, 0, len(results))
	latencyValues := make([]float64, 0, len(results))
	jitterValues := make([]float64, 0, len(results))

	for index, result := range results {
		if !sameConditions(first, result) {
			return Summary{}, fmt.Errorf("%w: run %d used different target, connections, duration or byte budget",
				ErrMixedConditions, index+1)
		}
		run := RunSummary{
			Index:       index,
			TestID:      result.TestID,
			Timestamp:   result.Timestamp,
			Status:      result.Status,
			Connections: result.Settings.Connections,
			Warnings:    result.Warnings,
		}
		switch result.Status {
		case StatusCompleted:
			summary.Completed++
		case StatusCancelled:
			summary.Cancelled++
		default:
			summary.Failed++
			run.ErrorMessage = result.ErrorMessage
		}
		if result.Download != nil {
			mbps := result.Download.Mbps
			bytes := result.Download.Bytes
			duration := result.Download.DurationNs
			run.DownloadMbps = &mbps
			run.DownloadBytes = &bytes
			run.DownloadDurationNs = &duration
			if result.Status == StatusCompleted {
				downloadValues = append(downloadValues, mbps)
			}
		}
		if result.Upload != nil {
			mbps := result.Upload.Mbps
			bytes := result.Upload.Bytes
			duration := result.Upload.DurationNs
			run.UploadMbps = &mbps
			run.UploadBytes = &bytes
			run.UploadDurationNs = &duration
			if result.Status == StatusCompleted {
				uploadValues = append(uploadValues, mbps)
			}
		}
		if result.Latency != nil {
			average := result.Latency.AverageNs
			run.LatencyAverageNs = &average
			if result.Status == StatusCompleted {
				latencyValues = append(latencyValues, float64(average)/float64(time.Millisecond))
			}
			if result.Latency.JitterNs != nil {
				jitter := *result.Latency.JitterNs
				run.JitterNs = &jitter
				if result.Status == StatusCompleted {
					jitterValues = append(jitterValues, float64(jitter)/float64(time.Millisecond))
				}
			}
		}
		summary.RunDetails = append(summary.RunDetails, run)
		summary.Warnings = append(summary.Warnings, result.Warnings...)
	}

	summary.DownloadMbps = metricsFromValues(downloadValues)
	summary.UploadMbps = metricsFromValues(uploadValues)
	summary.LatencyMs = metricsFromValues(latencyValues)
	summary.JitterMs = metricsFromValues(jitterValues)
	return summary, nil
}

func sameConditions(first, other Result) bool {
	return first.Target.ServerID == other.Target.ServerID &&
		first.Target.ServerAddress == other.Target.ServerAddress &&
		first.Settings.Connections == other.Settings.Connections &&
		first.Settings.DurationNs == other.Settings.DurationNs &&
		first.Settings.MaxBytes == other.Settings.MaxBytes
}

// metricsFromValues computes descriptive statistics for one metric. Mean,
// median, min and max need one value; standard deviation and the coefficient
// of variation need two. Values that cannot be computed stay nil.
func metricsFromValues(values []float64) MetricStats {
	stats := MetricStats{Samples: len(values)}
	if len(values) == 0 {
		return stats
	}
	mean := meanFloat(values)
	median := medianFloat(values)
	minimum, maximum := minMaxFloat(values)
	stats.Mean = &mean
	stats.Median = &median
	stats.Min = &minimum
	stats.Max = &maximum
	if len(values) < 2 {
		return stats
	}
	stdDev := sampleStdDev(values, mean)
	stats.StdDev = &stdDev
	stats.StdDevKind = StdDevKindSample
	if mean > 0 {
		cv := stdDev / mean * 100
		stats.CVPercent = &cv
	}
	return stats
}
