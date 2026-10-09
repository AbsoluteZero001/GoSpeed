// Package config holds the GoSpeed runtime defaults that are shared by the
// CLI, the tests and future frontends.
//
// v0.2.0 keeps this intentionally small: node configuration lives in a JSON
// file (see internal/nodes), while these values describe how a measurement is
// executed and can all be overridden on the command line.
package config

import (
	"fmt"
	"time"
)

// Test holds the defaults for one test run. The zero value is not valid; use
// Default.
type Test struct {
	// Duration is the transfer window of the download and upload phases.
	Duration time.Duration
	// Timeout is the timeout of a single phase. It must be longer than
	// Duration so a transfer can still be confirmed by the server.
	Timeout time.Duration
	// Connections is the number of parallel connections per transfer phase.
	// The engine supports 1..speedtest.MaxConnections; 1, 4, 8 and 16 are the
	// values covered by the end to end tests.
	Connections int
	// SampleInterval is the cadence of the real time rate samples.
	SampleInterval time.Duration
	// LatencySamples is the number of HTTP RTT samples.
	LatencySamples int
	// LatencyInterval is the pause between latency samples.
	LatencyInterval time.Duration
	// MaxBytes stops each transfer after this many bytes. Zero means the
	// duration alone decides when a transfer ends.
	MaxBytes int64
	// Warmup sends one unmeasured health request before the first phase.
	Warmup bool
}

// Config is the top level application configuration.
type Config struct {
	Test Test
}

// Default returns the defaults used when no flag overrides them.
func Default() Config {
	return Config{
		Test: Test{
			Duration:        10 * time.Second,
			Timeout:         30 * time.Second,
			Connections:     1,
			SampleInterval:  200 * time.Millisecond,
			LatencySamples:  5,
			LatencyInterval: 100 * time.Millisecond,
			MaxBytes:        0,
			Warmup:          true,
		},
	}
}

// Validate rejects a configuration the engine could not execute honestly.
func (c Config) Validate() error {
	if c.Test.Duration <= 0 {
		return fmt.Errorf("config: test duration must be positive")
	}
	if c.Test.Timeout <= c.Test.Duration {
		return fmt.Errorf("config: test timeout %s must be longer than duration %s",
			c.Test.Timeout, c.Test.Duration)
	}
	if c.Test.Connections <= 0 {
		return fmt.Errorf("config: test connections must be positive")
	}
	if c.Test.SampleInterval <= 0 {
		return fmt.Errorf("config: sample interval must be positive")
	}
	if c.Test.LatencySamples <= 0 {
		return fmt.Errorf("config: latency samples must be positive")
	}
	if c.Test.LatencyInterval < 0 {
		return fmt.Errorf("config: latency interval must not be negative")
	}
	if c.Test.MaxBytes < 0 {
		return fmt.Errorf("config: max bytes must not be negative")
	}
	return nil
}
