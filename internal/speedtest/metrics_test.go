package speedtest

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestMbpsFormula(t *testing.T) {
	// 1,000,000 bytes over 8 seconds is exactly 1 Mbps.
	got, err := Mbps(1_000_000, 8*time.Second)
	if err != nil {
		t.Fatalf("Mbps returned error: %v", err)
	}
	if math.Abs(got-1) > 1e-9 {
		t.Fatalf("Mbps = %v, want 1", got)
	}

	bitsPerSecond, err := BitsPerSecond(1_000_000, time.Second)
	if err != nil {
		t.Fatalf("BitsPerSecond returned error: %v", err)
	}
	if math.Abs(bitsPerSecond-8_000_000) > 1e-6 {
		t.Fatalf("BitsPerSecond = %v, want 8000000", bitsPerSecond)
	}
}

func TestMbpsRejectsInvalidInput(t *testing.T) {
	if _, err := Mbps(-1, time.Second); !errors.Is(err, ErrInvalidBytes) {
		t.Fatalf("negative bytes error = %v, want ErrInvalidBytes", err)
	}
	if _, err := Mbps(1, 0); !errors.Is(err, ErrZeroDuration) {
		t.Fatalf("zero duration error = %v, want ErrZeroDuration", err)
	}
	if _, err := BitsPerSecond(1, -time.Second); !errors.Is(err, ErrZeroDuration) {
		t.Fatalf("negative duration error = %v, want ErrZeroDuration", err)
	}
	if _, err := MBps(-1, time.Second); !errors.Is(err, ErrInvalidBytes) {
		t.Fatalf("MBps negative bytes error = %v, want ErrInvalidBytes", err)
	}
	if _, err := MBps(1, 0); !errors.Is(err, ErrZeroDuration) {
		t.Fatalf("MBps zero duration error = %v, want ErrZeroDuration", err)
	}
	if _, err := MiBps(-1, time.Second); !errors.Is(err, ErrInvalidBytes) {
		t.Fatalf("MiBps negative bytes error = %v, want ErrInvalidBytes", err)
	}
	if _, err := MiBps(1, 0); !errors.Is(err, ErrZeroDuration) {
		t.Fatalf("MiBps zero duration error = %v, want ErrZeroDuration", err)
	}
}

func TestMBpsAndMiBpsUnitsDiffer(t *testing.T) {
	decimal, err := MBps(1_000_000, time.Second)
	if err != nil {
		t.Fatalf("MBps returned error: %v", err)
	}
	if math.Abs(decimal-1) > 1e-9 {
		t.Fatalf("MBps = %v, want 1", decimal)
	}
	binary, err := MiBps(1<<20, time.Second)
	if err != nil {
		t.Fatalf("MiBps returned error: %v", err)
	}
	if math.Abs(binary-1) > 1e-9 {
		t.Fatalf("MiBps = %v, want 1", binary)
	}
}

func TestRateTracker(t *testing.T) {
	start := time.Unix(0, 0)
	tracker := newRateTracker(start)

	snapshot := tracker.Add(1_000_000, start.Add(8*time.Second))
	if math.Abs(snapshot.AverageMbps-1) > 1e-9 {
		t.Fatalf("first average = %v, want 1", snapshot.AverageMbps)
	}
	if math.Abs(snapshot.InstantMbps-1) > 1e-9 {
		t.Fatalf("first instant = %v, want 1", snapshot.InstantMbps)
	}

	snapshot = tracker.Add(1_000_000, start.Add(10*time.Second))
	if snapshot.TotalBytes != 2_000_000 {
		t.Fatalf("total bytes = %d, want 2000000", snapshot.TotalBytes)
	}
	if math.Abs(snapshot.AverageMbps-1.6) > 1e-9 {
		t.Fatalf("second average = %v, want 1.6", snapshot.AverageMbps)
	}
	if math.Abs(snapshot.InstantMbps-4) > 1e-9 {
		t.Fatalf("second instant = %v, want 4", snapshot.InstantMbps)
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := FormatMbps(936.523); got != "936.52 Mbps" {
		t.Fatalf("FormatMbps = %q", got)
	}
	if got := FormatMillis(12_500 * time.Microsecond); got != "12.50 ms" {
		t.Fatalf("FormatMillis = %q", got)
	}
}
