package speedtest

import (
	"fmt"
	"time"
)

// BitsPerSecond converts a transferred byte count and an elapsed duration into
// bits per second.
//
// The engine measures elapsed time with time.Now()/time.Since so that the
// calculation uses Go's monotonic clock and is immune to wall-clock jumps.
func BitsPerSecond(bytes int64, elapsed time.Duration) (float64, error) {
	if bytes < 0 {
		return 0, fmt.Errorf("%w: got %d", ErrInvalidBytes, bytes)
	}
	if elapsed <= 0 {
		return 0, fmt.Errorf("%w: got %s", ErrZeroDuration, elapsed)
	}
	return float64(bytes) * 8 / elapsed.Seconds(), nil
}

// Mbps converts a transferred byte count and an elapsed duration into megabits
// per second, where one megabit is 1,000,000 bits.
func Mbps(bytes int64, elapsed time.Duration) (float64, error) {
	bitsPerSecond, err := BitsPerSecond(bytes, elapsed)
	if err != nil {
		return 0, err
	}
	return bitsPerSecond / 1e6, nil
}

// MBps converts a transferred byte count and an elapsed duration into decimal
// megabytes per second, where one megabyte is 1,000,000 bytes.
func MBps(bytes int64, elapsed time.Duration) (float64, error) {
	if bytes < 0 {
		return 0, fmt.Errorf("%w: got %d", ErrInvalidBytes, bytes)
	}
	if elapsed <= 0 {
		return 0, fmt.Errorf("%w: got %s", ErrZeroDuration, elapsed)
	}
	return float64(bytes) / elapsed.Seconds() / 1e6, nil
}

// MiBps converts a transferred byte count and an elapsed duration into
// mebibytes per second, where one mebibyte is 2^20 bytes.
func MiBps(bytes int64, elapsed time.Duration) (float64, error) {
	if bytes < 0 {
		return 0, fmt.Errorf("%w: got %d", ErrInvalidBytes, bytes)
	}
	if elapsed <= 0 {
		return 0, fmt.Errorf("%w: got %s", ErrZeroDuration, elapsed)
	}
	return float64(bytes) / elapsed.Seconds() / (1 << 20), nil
}

// RateSnapshot is one sample of an in-flight transfer.
type RateSnapshot struct {
	TotalBytes  int64
	Elapsed     time.Duration
	AverageMbps float64
	InstantMbps float64
}

// rateTracker accumulates transferred bytes and derives both the average rate
// over the whole window and the instantaneous rate since the previous call.
type rateTracker struct {
	start      time.Time
	lastTime   time.Time
	lastBytes  int64
	totalBytes int64
}

func newRateTracker(start time.Time) *rateTracker {
	return &rateTracker{start: start, lastTime: start}
}

// Add records bytes observed at the given instant and returns an updated
// snapshot. Callers pass the observation time explicitly so tests can drive the
// tracker deterministically.
func (t *rateTracker) Add(bytes int64, now time.Time) RateSnapshot {
	if bytes > 0 {
		t.totalBytes += bytes
	}
	snapshot := RateSnapshot{TotalBytes: t.totalBytes, Elapsed: now.Sub(t.start)}
	if average, err := Mbps(t.totalBytes, snapshot.Elapsed); err == nil {
		snapshot.AverageMbps = average
	}
	if interval := now.Sub(t.lastTime); interval > 0 {
		if instant, err := Mbps(t.totalBytes-t.lastBytes, interval); err == nil {
			snapshot.InstantMbps = instant
		}
	}
	t.lastTime = now
	t.lastBytes = t.totalBytes
	return snapshot
}

// FormatMbps renders a rate with the precision the CLI uses.
func FormatMbps(value float64) string {
	return fmt.Sprintf("%.2f Mbps", value)
}

// FormatMillis renders a duration in milliseconds with the precision the CLI
// uses for latency values.
func FormatMillis(value time.Duration) string {
	return fmt.Sprintf("%.2f ms", float64(value)/float64(time.Millisecond))
}
