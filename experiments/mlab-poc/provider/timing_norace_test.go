//go:build !race

package mlabpoc_test

// timingAllowanceFactor is 1 without the race detector: latency ceilings in
// this package apply as written. See timing_race_test.go for rationale.
const timingAllowanceFactor = 1
