package mlabpoc

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ErrByteBudgetExceeded is returned by a tracked connection when the
// configured wire-level byte budget is reached. It marks a locally enforced,
// experimental budget. It must never be presented as an ISP billing hard cap.
var ErrByteBudgetExceeded = errors.New("ndt7 wire byte budget exceeded")

// connRegistry tracks every net.Conn created during a single NDT7 direction
// run so that cancellation or budget exhaustion can force-close the underlying
// transport immediately instead of waiting for SDK I/O deadlines (7 s read /
// write deadline in ndt7-client-go v0.10.1).
//
// The registry uses the public websocket.Dialer extension point
// (Dialer.NetDialContext) and does not modify the SDK.
type connRegistry struct {
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed atomic.Bool

	// Wire-level byte counters. Counted at the socket layer:
	//   - ws://  : TCP payload bytes, including WebSocket framing overhead.
	//   - wss:// : TLS record bytes, additionally including TLS framing,
	//     padding and the TLS handshake bytes of the same connection.
	// Not visible at this layer and therefore never counted or limited:
	// TCP/IP header bytes, TCP retransmissions, and data sitting in kernel
	// socket buffers that has not been delivered to the application yet.
	bytesRead    atomic.Int64
	bytesWritten atomic.Int64

	readBudget  atomic.Int64 // 0 = unlimited
	writeBudget atomic.Int64 // 0 = unlimited

	budgetHit      atomic.Bool
	budgetObserved atomic.Int64 // unix nano timestamp of the first budget hit
}

func newConnRegistry(readBudget, writeBudget int64) *connRegistry {
	r := &connRegistry{conns: make(map[net.Conn]struct{})}
	r.readBudget.Store(readBudget)
	r.writeBudget.Store(writeBudget)
	return r
}

// register wraps and tracks a raw net.Conn dialed for the current run.
func (r *connRegistry) register(conn net.Conn) *trackedConn {
	tc := &trackedConn{registry: r, Conn: conn}
	r.mu.Lock()
	r.conns[conn] = struct{}{}
	r.mu.Unlock()
	return tc
}

// forceCloseAll closes every tracked underlying connection. It is idempotent
// and safe to call concurrently with pending I/O; closing a net.Conn wakes up
// any goroutine blocked in Read/Write on it, which is exactly what allows a
// cancelled run to abort blocked SDK I/O promptly.
func (r *connRegistry) forceCloseAll() {
	if r.closed.Swap(true) {
		return
	}
	r.mu.Lock()
	conns := make([]net.Conn, 0, len(r.conns))
	for conn := range r.conns {
		conns = append(conns, conn)
	}
	r.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (r *connRegistry) budgetExceededNow() {
	if r.budgetHit.CompareAndSwap(false, true) {
		r.budgetObserved.Store(time.Now().UnixNano())
		// Force-close so both directions of the socket (e.g. a blocked
		// counter-flow read during upload) stop immediately.
		r.forceCloseAll()
	}
}

// trackedConn wraps a net.Conn returned by the dial function. It counts every
// byte read from / written to the socket and enforces optional wire-level
// byte budgets.
type trackedConn struct {
	net.Conn
	registry *connRegistry
}

func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		total := c.registry.bytesRead.Add(int64(n))
		if budget := c.registry.readBudget.Load(); budget > 0 && total > budget {
			// Bytes already received cannot be un-received; stop reading and
			// close the connection now. The overrun is bounded by one read
			// buffer (plus one TLS record for wss://).
			c.registry.budgetExceededNow()
			if err == nil {
				err = ErrByteBudgetExceeded
			}
		}
	}
	return n, err
}

func (c *trackedConn) Write(p []byte) (int, error) {
	if budget := c.registry.writeBudget.Load(); budget > 0 {
		pending := c.registry.bytesWritten.Load()
		if pending+int64(len(p)) > budget {
			// Refuse the write before it reaches the network so the
			// application cannot actively push effective payload beyond the
			// budget. The refusal happens before c.Conn.Write, so no bytes of
			// this call are sent.
			c.registry.budgetExceededNow()
			return 0, ErrByteBudgetExceeded
		}
	}
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.registry.bytesWritten.Add(int64(n))
	}
	return n, err
}
