package speedtest

import "errors"

// Sentinel errors returned by the measurement engine. Callers should match
// them with errors.Is so that configuration problems, measurement failures
// and cancellation stay distinguishable.
var (
	// ErrInvalidOptions reports options that cannot be executed as requested.
	ErrInvalidOptions = errors.New("speedtest: invalid options")
	// ErrUnsupportedConnections reports a connection count that v0.1.0 cannot run.
	ErrUnsupportedConnections = errors.New("speedtest: connection counts other than 1 are not supported in v0.1.0")
	// ErrUnsupportedProtocol reports a protocol the engine cannot speak.
	ErrUnsupportedProtocol = errors.New("speedtest: unsupported protocol")
	// ErrInvalidTarget reports an incomplete or malformed test target.
	ErrInvalidTarget = errors.New("speedtest: invalid target")
	// ErrZeroDuration reports a rate computed over no elapsed time.
	ErrZeroDuration = errors.New("speedtest: elapsed duration must be greater than zero")
	// ErrInvalidBytes reports a negative byte count.
	ErrInvalidBytes = errors.New("speedtest: byte count must not be negative")
	// ErrNoDataTransferred reports a transfer that completed without payload.
	ErrNoDataTransferred = errors.New("speedtest: no data was transferred")
	// ErrIncompleteTransfer reports a transfer that ended before all requested
	// data was transferred.
	ErrIncompleteTransfer = errors.New("speedtest: transfer ended before all requested data was transferred")
	// ErrByteCountMismatch reports that the client and the server disagree
	// about how many bytes were transferred.
	ErrByteCountMismatch = errors.New("speedtest: client and server byte counts do not match")
	// ErrUnexpectedStatus reports a non-success HTTP status code.
	ErrUnexpectedStatus = errors.New("speedtest: unexpected HTTP status")
	// ErrAllSamplesFailed reports that every latency sample failed.
	ErrAllSamplesFailed = errors.New("speedtest: all latency samples failed")
)
