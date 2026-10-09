package speedtest

import (
	"math"
	"sort"
	"sync"
	"time"
)

// StdDevKindSample names the standard deviation formula used in statistics.
// GoSpeed publishes the sample standard deviation (n-1) and says so in the
// result instead of leaving the definition implicit.
const StdDevKindSample = "sample_n_minus_1"

// sampleRecorder converts cumulative byte counters into instantaneous and
// cumulative rates. It is owned by one sampler goroutine; tests drive it
// directly with synthetic clocks.
//
// The instantaneous rate always uses the real elapsed time between two
// observations, never the nominal sampling interval, so a late ticker does not
// distort the rate.
type sampleRecorder struct {
	phase     Phase
	started   time.Time
	lastAt    time.Time
	lastBytes int64
	samples   []Sample
}

// record appends one sample. windowStart is the moment the measured window
// opened (first byte of data); it is only used the first time.
func (r *sampleRecorder) record(now time.Time, bytes int64, active int, windowStart time.Time) (Sample, bool) {
	if r.started.IsZero() {
		r.started = windowStart
		r.lastAt = windowStart
		// Every byte counted so far arrived inside the window that just
		// opened, so the first interval starts from zero.
		r.lastBytes = 0
	}
	interval := now.Sub(r.lastAt)
	elapsed := now.Sub(r.started)
	if interval <= 0 || elapsed <= 0 {
		return Sample{}, false
	}
	deltaBytes := bytes - r.lastBytes
	if deltaBytes < 0 {
		// Counters are monotonic; a negative delta means the caller passed
		// inconsistent data and the sample would be fabricated.
		return Sample{}, false
	}
	current, err := Mbps(deltaBytes, interval)
	if err != nil {
		return Sample{}, false
	}
	average, err := Mbps(bytes, elapsed)
	if err != nil {
		return Sample{}, false
	}
	sample := Sample{
		Phase:             r.phase,
		Timestamp:         now.UTC(),
		ElapsedNs:         elapsed,
		BytesTransferred:  bytes,
		CurrentMbps:       current,
		AverageMbps:       average,
		ActiveConnections: active,
	}
	r.samples = append(r.samples, sample)
	r.lastAt = now
	r.lastBytes = bytes
	return sample, true
}

// sampler samples a running transfer phase on a fixed cadence.
//
// It runs in its own goroutine and only reads atomic counters, so the data
// path never calls user code and a slow progress callback cannot slow a
// transfer down. Ticker ticks that cannot be served are coalesced by the
// runtime, which is the explicit drop policy: the final aggregate and
// statistics are still computed from the counters, never from the samples.
type sampler struct {
	phase    Phase
	state    RunState
	interval time.Duration
	counters *transferCounters
	budget   budgetSpec
	opts     Options

	done chan struct{}
	wg   sync.WaitGroup

	mu       sync.Mutex
	recorder sampleRecorder
}

func newSampler(opts Options, phase Phase, counters *transferCounters, spec budgetSpec) *sampler {
	interval := opts.SampleInterval
	if interval <= 0 {
		interval = DefaultSampleInterval
	}
	return &sampler{
		phase:    phase,
		state:    StateForPhase(phase),
		interval: interval,
		counters: counters,
		budget:   spec,
		opts:     opts,
		done:     make(chan struct{}),
		recorder: sampleRecorder{phase: phase},
	}
}

func (s *sampler) start() {
	s.wg.Add(1)
	go s.run()
}

func (s *sampler) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	var next time.Time
	for {
		select {
		case <-s.done:
			return
		case now := <-ticker.C:
			startNs := s.counters.startedAt.Load()
			if startNs == 0 {
				// The measurement window has not opened yet: workers are still
				// doing DNS/TCP/TLS setup.
				continue
			}
			windowStart := time.Unix(0, startNs)
			if next.IsZero() {
				next = windowStart.Add(s.interval)
			}
			if now.Before(next) {
				continue
			}
			// Schedule from the real observation time so a delayed ticker does
			// not cause a burst of catch-up samples.
			next = now.Add(s.interval)
			s.observe(now, windowStart)
		}
	}
}

// observe records one sample and reports it. Tests call this directly.
func (s *sampler) observe(now time.Time, windowStart time.Time) {
	bytes := s.counters.totalBytes()
	active := s.counters.activeConnections()
	s.mu.Lock()
	sample, ok := s.recorder.record(now, bytes, active, windowStart)
	s.mu.Unlock()
	if !ok {
		return
	}
	emit(s.opts, Progress{
		State:             s.state,
		Phase:             s.phase,
		Stage:             StageProgress,
		PhaseStartedAt:    windowStart,
		Elapsed:           sample.ElapsedNs,
		Bytes:             sample.BytesTransferred,
		Mbps:              sample.AverageMbps,
		InstantMbps:       sample.CurrentMbps,
		ActiveConnections: sample.ActiveConnections,
		Budget:            progressBudget(s.budget, sample.ElapsedNs, sample.BytesTransferred, sample.CurrentMbps),
	})
}

// stop stops the sampler goroutine and returns the recorded samples.
func (s *sampler) stop() []Sample {
	close(s.done)
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	samples := make([]Sample, len(s.recorder.samples))
	copy(samples, s.recorder.samples)
	return samples
}

// Summarize computes descriptive statistics over the instantaneous rate of
// the given samples. Mean, median, min and max need at least one sample; the
// standard deviation and the coefficient of variation need at least two, and
// the coefficient of variation also needs a positive mean. Values that cannot
// be computed stay nil (JSON null / N/A).
func Summarize(samples []Sample) Statistics {
	values := make([]float64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample.CurrentMbps)
	}
	metrics := metricsFromValues(values)
	return Statistics{
		Samples:                       metrics.Samples,
		MeanMbps:                      metrics.Mean,
		MedianMbps:                    metrics.Median,
		MinMbps:                       metrics.Min,
		MaxMbps:                       metrics.Max,
		StdDevMbps:                    metrics.StdDev,
		CoefficientOfVariationPercent: metrics.CVPercent,
		StdDevKind:                    metrics.StdDevKind,
	}
}

func meanFloat(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func medianFloat(values []float64) float64 {
	sorted := sortedCopy(values)
	middle := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[middle]
	}
	return (sorted[middle-1] + sorted[middle]) / 2
}

func minMaxFloat(values []float64) (float64, float64) {
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}
	return minimum, maximum
}

// sampleStdDev returns the sample standard deviation (n-1 denominator).
func sampleStdDev(values []float64, mean float64) float64 {
	var sum float64
	for _, value := range values {
		delta := value - mean
		sum += delta * delta
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

func sortedCopy(values []float64) []float64 {
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	return sorted
}
