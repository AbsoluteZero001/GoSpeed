package speedtest

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSharedBudgetIsNeverOverdrawn(t *testing.T) {
	const budgetSize = 100_000
	budget := newSharedBudget(budgetSize)

	var wait sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				taken := budget.take(4096)
				if taken == 0 {
					return
				}
				mu.Lock()
				total += taken
				mu.Unlock()
			}
		}()
	}
	wait.Wait()

	if total != budgetSize {
		t.Fatalf("reserved bytes = %d, want %d", total, budgetSize)
	}
	if !budget.exhausted() {
		t.Fatal("budget should be exhausted")
	}
	if taken := budget.take(1); taken != 0 {
		t.Fatalf("take after exhaustion = %d, want 0", taken)
	}
}

func TestBudgetReaderRefundsUnusedReservation(t *testing.T) {
	budget := newSharedBudget(100)
	reader := &budgetReader{reader: strings.NewReader("short"), budget: budget}
	buffer := make([]byte, 100)
	n, err := reader.Read(buffer)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if n != 5 {
		t.Fatalf("read %d bytes, want 5", n)
	}
	if remaining := budget.remaining.Load(); remaining != 95 {
		t.Fatalf("remaining = %d, want 95 (unused reservation must be refunded)", remaining)
	}
}

func TestProgressBudget(t *testing.T) {
	durationBudget := progressBudget(budgetSpec{duration: 10 * time.Second}, 2*time.Second, 0, 0)
	assertStat(t, "duration fraction", durationBudget.Fraction, 0.2)
	if durationBudget.Remaining == nil || *durationBudget.Remaining != 8*time.Second {
		t.Fatalf("duration remaining = %v, want 8s", durationBudget.Remaining)
	}

	byteBudget := progressBudget(budgetSpec{maxBytes: 100 << 20}, time.Second, 50<<20, 80)
	assertStat(t, "byte fraction", byteBudget.Fraction, 0.5)
	if byteBudget.Remaining == nil {
		t.Fatal("byte remaining must be estimated from the current rate")
	}
	wantRemaining := time.Duration(float64(50<<20) / (80e6 / 8) * float64(time.Second))
	if math.Abs(float64(*byteBudget.Remaining-wantRemaining)) > float64(time.Millisecond) {
		t.Fatalf("byte remaining = %s, want about %s", *byteBudget.Remaining, wantRemaining)
	}

	unknown := progressBudget(budgetSpec{}, time.Second, 1024, 10)
	if unknown.Fraction != nil || unknown.Remaining != nil {
		t.Fatalf("unknown budget must stay N/A, got %+v", unknown)
	}
}

func TestAggregateWorkersAndWarnings(t *testing.T) {
	start := time.Unix(0, 0)
	results := []workerResult{
		{index: 0, bytes: 100, firstAt: start, lastAt: start.Add(time.Second), finishedAt: start.Add(time.Second)},
		{index: 1, bytes: 200, firstAt: start.Add(10 * time.Millisecond), finishedAt: start.Add(500 * time.Millisecond), err: errors.New("boom")},
		{index: 2, finishedAt: start.Add(time.Second), err: errors.New("refused")},
	}
	aggregate := aggregateWorkers(results)
	if aggregate.clientBytes != 300 {
		t.Fatalf("client bytes = %d, want 300", aggregate.clientBytes)
	}
	if aggregate.active != 1 || aggregate.failed != 2 {
		t.Fatalf("active/failed = %d/%d, want 1/2", aggregate.active, aggregate.failed)
	}
	if !aggregate.windowStart.Equal(start) {
		t.Fatalf("window start = %s, want %s", aggregate.windowStart, start)
	}
	if !aggregate.lastDataAt.Equal(start.Add(time.Second)) {
		t.Fatalf("last data = %s", aggregate.lastDataAt)
	}
	if len(aggregate.errors) != 2 {
		t.Fatalf("errors = %v, want 2 entries", aggregate.errors)
	}

	reports := buildConnectionReports(results, false)
	if len(reports) != 3 {
		t.Fatalf("reports = %d, want 3", len(reports))
	}
	var reportedBytes int64
	for _, report := range reports {
		reportedBytes += report.Bytes
	}
	if reportedBytes != aggregate.clientBytes {
		t.Fatalf("report bytes = %d, want %d (no double counting)", reportedBytes, aggregate.clientBytes)
	}
	if reports[0].State != ConnectionCompleted || reports[1].State != ConnectionFailed {
		t.Fatalf("unexpected states: %+v", reports)
	}

	warnings := transferWarnings(PhaseDownload, &TransferResult{
		Connections:       3,
		ActiveConnections: aggregate.active,
		FailedConnections: aggregate.failed,
		ConnectionReports: reports,
	})
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one", warnings)
	}
	if !strings.Contains(warnings[0], "2 of 3 connections failed") {
		t.Fatalf("warning = %q", warnings[0])
	}
	if transferWarnings(PhaseDownload, nil) != nil {
		t.Fatal("nil transfer must not produce warnings")
	}
}

func TestRunWorkersKeepsOrder(t *testing.T) {
	results := runWorkers(8, func(index int) workerResult {
		// Finish in reverse order to prove the result slice is not append based.
		time.Sleep(time.Duration(8-index) * time.Millisecond)
		return workerResult{index: index, bytes: int64(index)}
	})
	for index, result := range results {
		if result.index != index || result.bytes != int64(index) {
			t.Fatalf("results[%d] = %+v", index, result)
		}
	}
}
