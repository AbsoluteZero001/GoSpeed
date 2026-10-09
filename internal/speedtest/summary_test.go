package speedtest

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func completedResult(index int, downloadMbps, uploadMbps float64, latency, jitter time.Duration) Result {
	jitterValue := jitter
	return Result{
		TestID:    fmt.Sprintf("gs-test-%d", index),
		Timestamp: time.Unix(int64(index), 0).UTC(),
		Status:    StatusCompleted,
		Target: TargetInfo{
			ServerID:      "local",
			ServerName:    "Local Test Server",
			ServerAddress: "http://127.0.0.1:8080",
			Protocol:      ProtocolHTTP,
			NetworkScope:  ScopeLocal,
		},
		Settings: SettingsSnapshot{
			Connections:     1,
			DurationNs:      time.Second,
			MaxBytes:        1 << 20,
			SampleInterval:  DefaultSampleInterval,
			LatencySamples:  3,
			LatencyInterval: time.Millisecond,
		},
		Download: &TransferResult{Bytes: 1 << 20, DurationNs: time.Second, Mbps: downloadMbps},
		Upload:   &UploadResult{TransferResult: TransferResult{Bytes: 1 << 20, DurationNs: time.Second, Mbps: uploadMbps}},
		Latency: &LatencyResult{
			Type:              LatencyHTTPRTT,
			Attempts:          3,
			SuccessfulSamples: 3,
			MinNs:             latency,
			AverageNs:         latency,
			MaxNs:             latency,
			JitterNs:          &jitterValue,
			SamplesNs:         []time.Duration{latency},
		},
	}
}

func TestSummarizeRuns(t *testing.T) {
	results := []Result{
		completedResult(0, 100, 50, 10*time.Millisecond, time.Millisecond),
		completedResult(1, 200, 100, 20*time.Millisecond, 2*time.Millisecond),
		completedResult(2, 300, 150, 30*time.Millisecond, 3*time.Millisecond),
		{
			TestID:       "gs-test-3",
			Status:       StatusFailed,
			ErrorMessage: "server refused",
			Target:       results0Target(),
			Settings:     SettingsSnapshot{Connections: 1, DurationNs: time.Second, MaxBytes: 1 << 20},
		},
	}
	summary, err := SummarizeRuns(results)
	if err != nil {
		t.Fatalf("SummarizeRuns returned error: %v", err)
	}
	if summary.Runs != 4 || summary.Completed != 3 || summary.Failed != 1 || summary.Cancelled != 0 {
		t.Fatalf("counts = runs:%d completed:%d failed:%d cancelled:%d",
			summary.Runs, summary.Completed, summary.Failed, summary.Cancelled)
	}
	if summary.NodeID != "local" || summary.ServerAddress != "http://127.0.0.1:8080" {
		t.Fatalf("summary identity = %+v", summary)
	}
	assertStat(t, "download mean", summary.DownloadMbps.Mean, 200)
	assertStat(t, "download median", summary.DownloadMbps.Median, 200)
	assertStat(t, "download min", summary.DownloadMbps.Min, 100)
	assertStat(t, "download max", summary.DownloadMbps.Max, 300)
	assertStat(t, "download stddev", summary.DownloadMbps.StdDev, 100)
	assertStat(t, "download cv", summary.DownloadMbps.CVPercent, 50)
	if summary.DownloadMbps.StdDevKind != StdDevKindSample {
		t.Fatalf("stddev kind = %q", summary.DownloadMbps.StdDevKind)
	}
	assertStat(t, "upload mean", summary.UploadMbps.Mean, 100)
	assertStat(t, "upload stddev", summary.UploadMbps.StdDev, 50)
	assertStat(t, "latency mean", summary.LatencyMs.Mean, 20)
	assertStat(t, "latency min", summary.LatencyMs.Min, 10)
	assertStat(t, "latency max", summary.LatencyMs.Max, 30)
	assertStat(t, "jitter mean", summary.JitterMs.Mean, 2)

	if len(summary.RunDetails) != 4 {
		t.Fatalf("run details = %d, want 4", len(summary.RunDetails))
	}
	failed := summary.RunDetails[3]
	if failed.Status != StatusFailed || failed.ErrorMessage != "server refused" {
		t.Fatalf("failed run detail = %+v", failed)
	}
	if failed.DownloadMbps != nil {
		t.Fatalf("failed run must not contribute a download rate: %v", *failed.DownloadMbps)
	}
	if summary.RunDetails[0].DownloadMbps == nil || *summary.RunDetails[0].DownloadMbps != 100 {
		t.Fatalf("first run detail = %+v", summary.RunDetails[0])
	}
}

func results0Target() TargetInfo {
	return TargetInfo{
		ServerID:      "local",
		ServerName:    "Local Test Server",
		ServerAddress: "http://127.0.0.1:8080",
		Protocol:      ProtocolHTTP,
		NetworkScope:  ScopeLocal,
	}
}

func TestSummarizeRunsRejectsMixedConditions(t *testing.T) {
	first := completedResult(0, 100, 50, time.Millisecond, time.Millisecond)
	second := completedResult(1, 100, 50, time.Millisecond, time.Millisecond)
	second.Settings.Connections = 4
	if _, err := SummarizeRuns([]Result{first, second}); !errors.Is(err, ErrMixedConditions) {
		t.Fatalf("error = %v, want ErrMixedConditions", err)
	}
}

func TestSummarizeRunsRequiresResults(t *testing.T) {
	if _, err := SummarizeRuns(nil); err == nil {
		t.Fatal("expected an error without results")
	}
}

func TestSummarizeRunsHandlesMissingMetrics(t *testing.T) {
	first := completedResult(0, 100, 50, time.Millisecond, time.Millisecond)
	second := completedResult(1, 200, 100, time.Millisecond, time.Millisecond)
	second.Upload = nil
	second.Latency = nil
	summary, err := SummarizeRuns([]Result{first, second})
	if err != nil {
		t.Fatalf("SummarizeRuns returned error: %v", err)
	}
	if summary.UploadMbps.Samples != 1 {
		t.Fatalf("upload samples = %d, want 1 (a missing metric is not zero)", summary.UploadMbps.Samples)
	}
	if summary.UploadMbps.StdDev != nil {
		t.Fatalf("one value must not produce a standard deviation: %v", *summary.UploadMbps.StdDev)
	}
	if summary.LatencyMs.Samples != 1 {
		t.Fatalf("latency samples = %d, want 1", summary.LatencyMs.Samples)
	}
	if summary.DownloadMbps.Samples != 2 {
		t.Fatalf("download samples = %d, want 2", summary.DownloadMbps.Samples)
	}
}
