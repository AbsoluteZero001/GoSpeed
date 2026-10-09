package speedtest

import "testing"

// normalizedOptions prepares options the way Engine.Run does, so phase
// functions can be tested without going through the whole run.
func normalizedOptions(t *testing.T, options Options) Options {
	t.Helper()
	normalized, err := options.normalized()
	if err != nil {
		t.Fatalf("options.normalized() error = %v", err)
	}
	return normalized
}
