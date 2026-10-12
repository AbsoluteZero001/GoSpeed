package main

import (
	"context"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/speedtest"
)

// This file keeps the pre-B4 desktop call path alive as a test-only reference.
//
// Before B4 the runner called speedtest.Engine directly. B4 routes the same
// measurement through provider.Registry. The compatibility tests run both paths
// against the same local server and compare them, so a regression that routing
// introduced cannot hide. Nothing in the shipped binary calls these helpers.

// engineOptions builds the measurement request. Warmup, capability
// negotiation and latency sampling keep their engine defaults, so a GUI run
// and a CLI run with the same duration, connections and sample interval
// execute the same measurement.
func engineOptions(target speedtest.Target, options StartOptions, progress func(speedtest.Progress)) speedtest.Options {
	duration := time.Duration(options.DurationMs) * time.Millisecond
	timeout := duration + timeoutSlack
	if timeout < speedtest.DefaultTimeout {
		timeout = speedtest.DefaultTimeout
	}
	return speedtest.Options{
		Target:                target,
		Phases:                speedtest.PhasesAll,
		Duration:              duration,
		MaxBytes:              0,
		Timeout:               timeout,
		Connections:           options.Connections,
		LatencySamples:        speedtest.DefaultLatencySamples,
		LatencyInterval:       speedtest.DefaultLatencyInterval,
		Warmup:                true,
		NegotiateCapabilities: true,
		SampleInterval:        time.Duration(options.SampleIntervalMs) * time.Millisecond,
		Progress:              progress,
	}
}

// legacyMeasurementEngine is the interface the runner used before B4.
type legacyMeasurementEngine interface {
	Run(ctx context.Context, options speedtest.Options) (speedtest.Result, error)
}

// newLegacyEngine builds the old direct engine path for comparison.
func newLegacyEngine() legacyMeasurementEngine {
	return speedtest.NewEngine(speedtest.NewHTTPClient())
}

// runLegacyMeasurement performs one measurement the way the v0.4.0 desktop
// client did, so a test can compare it with the provider path.
func runLegacyMeasurement(ctx context.Context, target speedtest.Target, options StartOptions, progress func(speedtest.Progress)) (speedtest.Result, error) {
	return newLegacyEngine().Run(ctx, engineOptions(target, options, progress))
}

var _ = time.Second
