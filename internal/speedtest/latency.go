package speedtest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

// latencyErrorLimit bounds how many sample failures are kept in the result.
const latencyErrorLimit = 5

// measureLatency collects HTTP RTT samples. The measured quantity is the time
// from issuing the request until the first response byte arrives, which
// includes DNS resolution, TCP and TLS setup on the first sample and only the
// request/response round trip afterwards because the connection is reused.
func (e *Engine) measureLatency(ctx context.Context, opts Options) (*LatencyResult, error) {
	result := &LatencyResult{
		Type:      LatencyHTTPRTT,
		Attempts:  opts.LatencySamples,
		SamplesNs: make([]time.Duration, 0, opts.LatencySamples),
	}
	emit(opts, Progress{Phase: PhaseLatency, Stage: StageStart})

	for index := 0; index < opts.LatencySamples; index++ {
		if index > 0 && opts.LatencyInterval > 0 {
			if err := sleepContext(ctx, opts.LatencyInterval); err != nil {
				return result, fmt.Errorf("%s: %w", PhaseLatency, err)
			}
		}
		sampleCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		sample, err := pingOnce(sampleCtx, e.client, opts.Target)
		cancel()
		if err != nil {
			// A cancelled or expired parent context aborts the whole phase;
			// an individual failed request is recorded and sampling continues.
			if ctx.Err() != nil {
				return result, phaseError(PhaseLatency, ctx, sampleCtx, opts.Timeout, err)
			}
			result.FailedSamples++
			if len(result.Errors) < latencyErrorLimit {
				result.Errors = append(result.Errors, err.Error())
			}
			continue
		}
		result.SamplesNs = append(result.SamplesNs, sample)
		emit(opts, Progress{
			Phase:   PhaseLatency,
			Stage:   StageProgress,
			Elapsed: sample,
			Bytes:   int64(len(result.SamplesNs)),
			Message: "sample",
		})
	}

	result.SuccessfulSamples = len(result.SamplesNs)
	if result.SuccessfulSamples == 0 {
		return result, fmt.Errorf("%w: %d attempts failed, first error: %s",
			ErrAllSamplesFailed, result.Attempts, firstError(result.Errors))
	}
	result.MinNs, result.MaxNs, result.AverageNs = latencyStats(result.SamplesNs)
	if jitter, ok := jitterStats(result.SamplesNs); ok {
		result.JitterNs = &jitter
	}
	emit(opts, Progress{
		Phase:   PhaseLatency,
		Stage:   StageDone,
		Elapsed: result.AverageNs,
		Mbps:    0,
		Message: string(LatencyHTTPRTT),
	})
	return result, nil
}

// pingOnce issues one /ping request and returns the HTTP RTT.
func pingOnce(ctx context.Context, client *http.Client, target Target) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.BaseURL+"/ping", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Cache-Control", "no-store")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()
	// time.Since uses the monotonic clock, so the sample cannot be distorted by
	// a wall-clock adjustment in the middle of the request.
	rtt := time.Since(start)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return 0, fmt.Errorf("%w: GET /ping: %s", ErrUnexpectedStatus, resp.Status)
	}
	return rtt, nil
}

// latencyStats returns the minimum, maximum and arithmetic mean of the
// samples. The caller guarantees a non-empty slice.
func latencyStats(samples []time.Duration) (min, max, average time.Duration) {
	min, max = samples[0], samples[0]
	var sum time.Duration
	for _, sample := range samples {
		if sample < min {
			min = sample
		}
		if sample > max {
			max = sample
		}
		sum += sample
	}
	return min, max, sum / time.Duration(len(samples))
}

// jitterStats returns the mean absolute difference between consecutive
// samples. It reports false when there is nothing to compare.
func jitterStats(samples []time.Duration) (time.Duration, bool) {
	if len(samples) < 2 {
		return 0, false
	}
	var total time.Duration
	for index := 1; index < len(samples); index++ {
		delta := samples[index] - samples[index-1]
		if delta < 0 {
			delta = -delta
		}
		total += delta
	}
	return total / time.Duration(len(samples)-1), true
}

func firstError(errorsList []string) string {
	if len(errorsList) == 0 {
		return "no error details"
	}
	return errorsList[0]
}

// sleepContext waits for d unless ctx is cancelled first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
