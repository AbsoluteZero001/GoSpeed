//go:build race

package mlabpoc_test

// timingAllowanceFactor scales the prompt-cancellation latency ceilings in
// this package. The Go race detector instruments every memory access and
// synchronization primitive on these sync-heavy code paths (context
// broadcast, conntracker mutex, channel hand-off, websocket reader wake-up),
// adding 2x-20x overhead under load spikes. A single observed flake showed a
// cancel latency above the fixed 500 ms ceiling on a fully passing,
// race-free run, so the allowance is scaled for race builds only.
//
// The factor is deliberately bounded (not unbounded relaxation): any real
// cancellation regression still fails the status/error-class assertions,
// and a broken force-close path ends at the SDK 7 s I/O deadline, far above
// every scaled ceiling (max 8 s applies to the timeout test whose
// functional correctness is carried by its status assertions).
const timingAllowanceFactor = 4
