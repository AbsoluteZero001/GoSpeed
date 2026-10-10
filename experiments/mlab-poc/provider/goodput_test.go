package mlabpoc

import (
	"math"
	"testing"
)

// TestGoodputMbpsUnitRegression pins the P0-L unit fix with the sample from
// the first real public measurement: 2,097,152 application bytes over
// 10,391 ms is about 1.61459 Mbps. The pre-fix expression
// (bytes*8*1_000_000/elapsedMs/1_000_000) computed kbit/s and returned
// 1614.59 under the Mbps name.
func TestGoodputMbpsUnitRegression(t *testing.T) {
	got := goodputMbps(2097152, 10391)
	const want = 1.614591
	if math.Abs(got-want) > 1e-5 {
		t.Fatalf("goodputMbps(2097152, 10391) = %f Mbps, want ~%f (pre-fix bug returned 1614.59)", got, want)
	}
}

// TestApplyProgressGoodputUnitsAndGuards checks that applyProgress publishes
// true Mbit/s on all three rate paths and refuses zero/negative elapsed
// windows and non-positive byte counts instead of dividing by zero.
func TestApplyProgressGoodputUnitsAndGuards(t *testing.T) {
	const sampleBytes = int64(2097152)
	const sampleMs = int64(10391)
	const want = 1.614591

	t.Run("download path", func(t *testing.T) {
		result := &Result{Direction: DirectionDownload}
		applyProgress(result, Progress{ApplicationBytes: sampleBytes, ElapsedMs: sampleMs})
		if result.DownloadGoodputMbps == nil || math.Abs(*result.DownloadGoodputMbps-want) > 1e-5 {
			t.Fatalf("download goodput = %v, want ~%f", result.DownloadGoodputMbps, want)
		}
	})

	t.Run("upload server counter path", func(t *testing.T) {
		result := &Result{Direction: DirectionUpload}
		received := sampleBytes
		applyProgress(result, Progress{
			Origin:           "server",
			TCPBytesReceived: &received,
			ElapsedMs:        sampleMs,
		})
		if result.UploadGoodputMbps == nil || math.Abs(*result.UploadGoodputMbps-want) > 1e-5 {
			t.Fatalf("upload goodput = %v, want ~%f", result.UploadGoodputMbps, want)
		}
	})

	t.Run("upload client bytes path", func(t *testing.T) {
		result := &Result{Direction: DirectionUpload}
		applyProgress(result, Progress{ApplicationBytes: sampleBytes, ElapsedMs: sampleMs})
		if result.UploadGoodputMbps == nil || math.Abs(*result.UploadGoodputMbps-want) > 1e-5 {
			t.Fatalf("upload goodput = %v, want ~%f", result.UploadGoodputMbps, want)
		}
	})

	t.Run("zero and invalid inputs are refused", func(t *testing.T) {
		download := &Result{Direction: DirectionDownload}
		applyProgress(download, Progress{ApplicationBytes: sampleBytes, ElapsedMs: 0})
		applyProgress(download, Progress{ApplicationBytes: sampleBytes, ElapsedMs: -5})
		applyProgress(download, Progress{ApplicationBytes: 0, ElapsedMs: sampleMs})
		applyProgress(download, Progress{ApplicationBytes: -1, ElapsedMs: sampleMs})
		if download.DownloadGoodputMbps != nil {
			t.Fatalf("invalid input must not set download goodput, got %v", *download.DownloadGoodputMbps)
		}

		upload := &Result{Direction: DirectionUpload}
		zero := int64(0)
		negative := int64(-1)
		applyProgress(upload, Progress{Origin: "server", TCPBytesReceived: &zero, ElapsedMs: 0})
		applyProgress(upload, Progress{Origin: "server", TCPBytesReceived: &negative, ElapsedMs: sampleMs})
		applyProgress(upload, Progress{Origin: "server", TCPBytesReceived: &zero, ElapsedMs: sampleMs})
		applyProgress(upload, Progress{ApplicationBytes: sampleBytes, ElapsedMs: 0})
		if upload.UploadGoodputMbps != nil {
			t.Fatalf("invalid input must not set upload goodput, got %v", *upload.UploadGoodputMbps)
		}
	})
}
