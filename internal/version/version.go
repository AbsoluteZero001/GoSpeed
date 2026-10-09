// Package version exposes GoSpeed build information.
package version

import "runtime"

// Version is the semantic version of this GoSpeed release.
const Version = "0.4.0"

// UserAgent is the HTTP User-Agent the client sends to test servers.
func UserAgent() string {
	return "GoSpeed/" + Version
}

// String returns a human readable version summary including the toolchain
// that produced the binary.
func String() string {
	return Version + " (" + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}
