//go:build !windows && !linux && !darwin

package netdiag

// platformPing reports that no supported ping invocation exists on this
// platform.
//
// It returns false rather than a runner that always fails, so the caller can
// say "this platform has no usable ping" instead of implying a measurement was
// attempted and happened to fail.
func platformPing() (pingRunner, bool) {
	return nil, false
}
