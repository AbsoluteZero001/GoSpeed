package speedtest

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MaxConnections bounds the number of parallel connections per transfer phase.
// Values outside [1, MaxConnections] are rejected instead of silently clamped.
const MaxConnections = 16

// connectionErrorLimit bounds how many per connection errors are kept in a
// warning so a broken server cannot blow up the result size.
const connectionErrorLimit = 4

// transferCounters aggregates the byte counters of one transfer phase. Every
// byte is added exactly once, by the worker that transferred it, so the
// aggregate can never double count.
type transferCounters struct {
	total  atomic.Int64
	active atomic.Int32
	// started holds the earliest data observation of the shared window as a
	// complete time.Time. Keeping the value (instead of converting it to Unix
	// nanoseconds) preserves its monotonic clock reading, so window durations
	// never fall back to the wall clock and cannot be corrupted by a clock
	// jump.
	started atomic.Pointer[time.Time]
}

func (c *transferCounters) addBytes(bytes int64) {
	if bytes > 0 {
		c.total.Add(bytes)
	}
}

func (c *transferCounters) totalBytes() int64 {
	return c.total.Load()
}

func (c *transferCounters) activeConnections() int {
	return int(c.active.Load())
}

func (c *transferCounters) beginConnection() {
	c.active.Add(1)
}

func (c *transferCounters) endConnection() {
	c.active.Add(-1)
}

// markStarted opens the shared measurement window at the earliest observed
// data time. CompareAndSwap keeps the minimum even when workers race and
// arrive out of order.
func (c *transferCounters) markStarted(at time.Time) {
	if at.IsZero() {
		return
	}
	for {
		current := c.started.Load()
		if current != nil && !at.Before(*current) {
			return
		}
		candidate := at
		if c.started.CompareAndSwap(current, &candidate) {
			return
		}
	}
}

// windowStart returns the shared window start. The second result is false
// until the first data of the phase has been observed.
func (c *transferCounters) windowStart() (time.Time, bool) {
	if pointer := c.started.Load(); pointer != nil {
		return *pointer, true
	}
	return time.Time{}, false
}

// windowDuration validates a measurement window before a rate is derived from
// it. A window that is missing, empty or inverted means the platform clock
// could not resolve the transfer; the measurement must be reported as invalid
// instead of producing a fabricated rate. The caller adds the phase context.
func windowDuration(start, end time.Time) (time.Duration, error) {
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return 0, ErrZeroDuration
	}
	return end.Sub(start), nil
}

// sharedBudget is a byte budget shared by every worker of one phase. Workers
// reserve bytes before reading or generating them, so concurrent connections
// can never overdraw the budget and a multi connection run stays comparable
// with a single connection run.
type sharedBudget struct {
	initial   int64
	remaining atomic.Int64
}

func newSharedBudget(total int64) *sharedBudget {
	budget := &sharedBudget{initial: total}
	budget.remaining.Store(total)
	return budget
}

// take reserves up to requested bytes and returns how many were reserved.
func (b *sharedBudget) take(requested int) int {
	for {
		remaining := b.remaining.Load()
		if remaining <= 0 {
			return 0
		}
		take := int64(requested)
		if take > remaining {
			take = remaining
		}
		if b.remaining.CompareAndSwap(remaining, remaining-take) {
			return int(take)
		}
	}
}

// refund returns a reservation that produced no bytes.
func (b *sharedBudget) refund(bytes int) {
	if bytes > 0 {
		b.remaining.Add(int64(bytes))
	}
}

func (b *sharedBudget) exhausted() bool {
	return b.remaining.Load() <= 0
}

// budgetReader caps one connection with a budget shared by all workers. Bytes
// are reserved before the read, so the aggregate cannot exceed the budget even
// when many connections read concurrently.
type budgetReader struct {
	reader io.Reader
	budget *sharedBudget
	// exhausted is set when this reader stopped because the shared budget had
	// no bytes left. The shared counter cannot be consulted afterwards: other
	// connections refund unused reservations, so a later read of the counter
	// can show bytes again and misclassify a normal budget stop as an early
	// server close. Only the worker goroutine that owns this reader touches it.
	exhausted bool
}

func (r *budgetReader) Read(p []byte) (int, error) {
	if r.budget == nil {
		return r.reader.Read(p)
	}
	allowed := r.budget.take(len(p))
	if allowed == 0 {
		r.exhausted = true
		return 0, io.EOF
	}
	n, err := r.reader.Read(p[:allowed])
	if n < allowed {
		r.budget.refund(allowed - n)
	}
	return n, err
}

// exhaustedByBudget reports whether this connection stopped because the shared
// byte budget was drained.
func (r *budgetReader) exhaustedByBudget() bool {
	return r.exhausted
}

// countingSink counts bytes as they are read into it. Write never touches the
// network or a callback, so it stays off the critical path.
type countingSink struct {
	onWrite func(bytes int64)
}

func (s *countingSink) Write(p []byte) (int, error) {
	if s.onWrite != nil {
		s.onWrite(int64(len(p)))
	}
	return len(p), nil
}

// workerResult is the internal outcome of one connection.
type workerResult struct {
	index int
	bytes int64
	// firstAt is when this connection saw its first transferred byte,
	// lastAt when it saw its last one, finishedAt when it stopped working.
	firstAt    time.Time
	lastAt     time.Time
	finishedAt time.Time
	err        error
	// earlyEOF records that this connection saw a natural end of stream before
	// the duration window ended. The phase decides afterwards whether that is a
	// real truncation: in byte-budget mode the last read can return data and EOF
	// together, and the aggregate byte count is the authoritative completion
	// signal.
	earlyEOF bool
	// upload only
	serverConfirmed int64
	serverDuration  time.Duration
}

// runWorkers starts exactly count workers concurrently and waits for all of
// them. Each worker writes its own slot, so no lock is needed and the results
// stay ordered by worker index.
func runWorkers(count int, worker func(index int) workerResult) []workerResult {
	results := make([]workerResult, count)
	var wait sync.WaitGroup
	wait.Add(count)
	for index := 0; index < count; index++ {
		go func(index int) {
			defer wait.Done()
			results[index] = worker(index)
		}(index)
	}
	wait.Wait()
	return results
}

// transferAggregate combines worker outcomes without double counting and keeps
// requested, active and failed connections separate.
type transferAggregate struct {
	clientBytes       int64
	serverConfirmed   int64
	active            int
	failed            int
	windowStart       time.Time
	lastDataAt        time.Time
	finishedAt        time.Time
	serverDurationMax time.Duration
	errors            []string
}

func aggregateWorkers(results []workerResult) transferAggregate {
	aggregate := transferAggregate{}
	for _, result := range results {
		aggregate.clientBytes += result.bytes
		aggregate.serverConfirmed += result.serverConfirmed
		if result.err == nil {
			aggregate.active++
		} else {
			aggregate.failed++
			if len(aggregate.errors) < connectionErrorLimit {
				aggregate.errors = append(aggregate.errors,
					fmt.Sprintf("connection %d: %v", result.index+1, result.err))
			}
		}
		if !result.firstAt.IsZero() && (aggregate.windowStart.IsZero() || result.firstAt.Before(aggregate.windowStart)) {
			aggregate.windowStart = result.firstAt
		}
		if !result.lastAt.IsZero() && result.lastAt.After(aggregate.lastDataAt) {
			aggregate.lastDataAt = result.lastAt
		}
		if result.finishedAt.After(aggregate.finishedAt) {
			aggregate.finishedAt = result.finishedAt
		}
		if result.serverDuration > aggregate.serverDurationMax {
			aggregate.serverDurationMax = result.serverDuration
		}
	}
	return aggregate
}

// buildConnectionReports converts worker outcomes into the per connection
// evidence stored in the result. Failed connections keep their partial byte
// count so nothing is silently dropped.
func buildConnectionReports(results []workerResult, includeServerBytes bool) []ConnectionReport {
	reports := make([]ConnectionReport, 0, len(results))
	for _, result := range results {
		report := ConnectionReport{
			Index: result.index,
			State: ConnectionCompleted,
			Bytes: result.bytes,
		}
		if !result.firstAt.IsZero() && result.finishedAt.After(result.firstAt) {
			report.DurationNs = result.finishedAt.Sub(result.firstAt)
		}
		if result.err != nil {
			report.State = ConnectionFailed
			report.Error = result.err.Error()
		}
		if includeServerBytes {
			confirmed := result.serverConfirmed
			report.ServerConfirmedBytes = &confirmed
			report.ServerDurationNs = result.serverDuration
		}
		reports = append(reports, report)
	}
	return reports
}

// transferWarnings turns partial connection failures into warning text. The
// aggregate numbers stay untouched: a partial result is reported as partial,
// never as a clean result.
func transferWarnings(phase Phase, transfer *TransferResult) []string {
	if transfer == nil || transfer.FailedConnections == 0 {
		return nil
	}
	details := make([]string, 0, connectionErrorLimit)
	for _, report := range transfer.ConnectionReports {
		if report.Error == "" {
			continue
		}
		details = append(details, fmt.Sprintf("connection %d: %s", report.Index+1, report.Error))
		if len(details) == connectionErrorLimit {
			break
		}
	}
	detail := "no error details"
	if len(details) > 0 {
		detail = strings.Join(details, "; ")
	}
	return []string{fmt.Sprintf(
		"%s: %d of %d connections failed (%d active); throughput is based only on the bytes that were actually transferred: %s",
		phase, transfer.FailedConnections, transfer.Connections, transfer.ActiveConnections, detail)}
}

func firstWorkerError(results []workerResult) error {
	for _, result := range results {
		if result.err != nil {
			return result.err
		}
	}
	return nil
}

// budgetSpec describes how much a phase is allowed to transfer.
type budgetSpec struct {
	duration time.Duration
	maxBytes int64
}

// progressBudget turns the remaining duration and byte budget into a progress
// fraction and a remaining time estimate. The fraction is the larger of the
// two because the phase stops when either budget is exhausted. A nil field
// means the value is unknown and must be rendered as N/A, never as zero.
func progressBudget(spec budgetSpec, elapsed time.Duration, bytes int64, currentMbps float64) Budget {
	var budget Budget
	var fraction float64
	hasFraction := false
	var remaining time.Duration
	hasRemaining := false

	if spec.duration > 0 {
		fraction = elapsed.Seconds() / spec.duration.Seconds()
		hasFraction = true
		remaining = spec.duration - elapsed
		if remaining < 0 {
			remaining = 0
		}
		hasRemaining = true
	}
	if spec.maxBytes > 0 {
		byteFraction := float64(bytes) / float64(spec.maxBytes)
		if !hasFraction || byteFraction > fraction {
			fraction = byteFraction
		}
		hasFraction = true
		if currentMbps > 0 && bytes < spec.maxBytes {
			bytesPerSecond := currentMbps * 1e6 / 8
			estimate := time.Duration(float64(spec.maxBytes-bytes) / bytesPerSecond * float64(time.Second))
			if !hasRemaining || estimate < remaining {
				remaining = estimate
				hasRemaining = true
			}
		}
	}
	if hasFraction {
		if fraction < 0 {
			fraction = 0
		}
		if fraction > 1 {
			fraction = 1
		}
		budget.Fraction = &fraction
	}
	if hasRemaining {
		budget.Remaining = &remaining
	}
	return budget
}
