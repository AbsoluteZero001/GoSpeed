// Package mock is a localhost WebSocket fixture for the ndt7 production
// provider. It is the testing counterpart to internal/provider/ndt7
// and intentionally small: the production tests only need to confirm
// the protocol contract that a real NDT7 server fulfils, not exercise
// every M-Lab feature.
//
// The mock uses gorilla/websocket because that is the production
// client library and the wire format is identical to a real NDT7
// server. The fixture refuses to upgrade a request that does not
// advertise the NDT7 Sec-WebSocket-Protocol, so a misconfigured
// client surfaces as a handshake error.
package mock

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"

	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// RequiredSubprotocol is the Sec-WebSocket-Protocol value the ndt7
// provider must send. The mock only accepts this exact string.
const RequiredSubprotocol = "net.measurementlab.ndt.v7"

// Scenario names the behaviour the mock exposes for one upgrade
// request. Scenarios are deliberately minimal; production tests only
// need to confirm the protocol contract, not exercise every failure
// mode.
type Scenario string

const (
	// ScenarioDownload serves a fixed amount of binary data and
	// emits NDT7 measurement JSON frames in lockstep so the client
	// can count bytes and observe the protocol at the same time.
	ScenarioDownload Scenario = "download"
	// ScenarioUpload reads binary data the client streams until
	// either the configured limit or the request context expires.
	ScenarioUpload Scenario = "upload"
	// ScenarioHandshakeFailure rejects every upgrade with HTTP 403
	// before the WebSocket handshake completes. This is how the
	// official NDT7 server reports a failed Locate response today.
	ScenarioHandshakeFailure Scenario = "handshake_failure"
	// ScenarioNetworkDisconnect accepts the upgrade, writes one byte,
	// then closes the underlying TCP connection without a WebSocket
	// close frame. The client's first read after that returns an
	// error and the measurement channel closes.
	ScenarioNetworkDisconnect Scenario = "network_disconnect"
)

// Server is a localhost mock that satisfies the NDT7 protocol minimum
// the GoSpeed NDT7 provider needs to verify. It never reaches a real
// NDT7 server and never accepts a non-loopback peer.
type Server struct {
	scenario Scenario

	// bytesTarget bounds the ScenarioUpload body. ScenarioDownload
	// always sends the fixed block in download().
	bytesTarget int64

	// chunkDelay is a per-chunk pacing delay. 0 = no delay (the
	// default). Cancellation and timeout tests set it to a value
	// that keeps the server busy long enough to observe the
	// cancel/timeout.
	chunkDelay time.Duration

	server *httptest.Server

	requests    atomic.Int64
	connections atomic.Int64
	active      atomic.Int64

	bytesRead    atomic.Int64
	bytesWritten atomic.Int64

	stopOnce sync.Once
	stop     chan struct{}
}

// New returns a mock server for one scenario. bytesTarget bounds the
// upload scenario; other scenarios ignore it.
func New(scenario Scenario, bytesTarget int64) *Server {
	return NewWithChunkDelay(scenario, bytesTarget, 0)
}

// NewWithChunkDelay is New with a per-chunk pacing delay. Tests that
// need to observe cancellation or timeout during a download pass a
// non-zero delay so the run stays busy long enough for the
// cancel/timeout to fire.
func NewWithChunkDelay(scenario Scenario, bytesTarget int64, chunkDelay time.Duration) *Server {
	s := &Server{
		scenario:    scenario,
		bytesTarget: bytesTarget,
		chunkDelay:  chunkDelay,
		stop:        make(chan struct{}),
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// Close stops the mock and releases the listener.
func (s *Server) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.server.Close()
}

// Host returns host:port for ad-hoc parsing.
func (s *Server) Host() string { return s.server.Listener.Addr().String() }

// Snapshot reports aggregate counters the test can assert against.
type Snapshot struct {
	Requests     int64 `json:"requests"`
	Connections  int64 `json:"connections"`
	Active       int64 `json:"active"`
	BytesRead    int64 `json:"bytesRead"`
	BytesWritten int64 `json:"bytesWritten"`
}

func (s *Server) Snapshot() Snapshot {
	return Snapshot{
		Requests:     s.requests.Load(),
		Connections:  s.connections.Load(),
		Active:       s.active.Load(),
		BytesRead:    s.bytesRead.Load(),
		BytesWritten: s.bytesWritten.Load(),
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	if s.scenario == ScenarioHandshakeFailure {
		http.Error(w, "mock handshake rejected", http.StatusForbidden)
		return
	}
	upgrader := websocket.Upgrader{
		Subprotocols: []string{RequiredSubprotocol},
		CheckOrigin:  func(*http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	s.connections.Add(1)
	s.active.Add(1)
	defer s.active.Add(-1)

	switch s.scenario {
	case ScenarioDownload:
		s.download(conn)
	case ScenarioUpload:
		s.upload(conn)
	case ScenarioNetworkDisconnect:
		_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 1))
		s.bytesWritten.Add(1)
		_ = conn.UnderlyingConn().Close()
	default:
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseProtocolError, "unknown scenario"),
			time.Now().Add(time.Second),
		)
	}
}

// download sends a fixed amount of binary data in 16 KiB frames and
// intersperses NDT7 measurement JSON frames so the client can count
// bytes and observe the protocol at the same time.
func (s *Server) download(conn *websocket.Conn) {
	const chunk = 16 * 1024
	const chunks = 16
	block := make([]byte, chunk)
	for i := 0; i < chunks; i++ {
		if err := conn.WriteMessage(websocket.BinaryMessage, block); err != nil {
			return
		}
		s.bytesWritten.Add(int64(chunk))
		if err := writeMeasurement(conn, int64((i+1)*chunk), int64((i+1)*chunk)); err != nil {
			return
		}
		if s.chunkDelay > 0 {
			select {
			case <-s.stop:
				return
			case <-time.After(s.chunkDelay):
			}
		}
	}
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "mock complete"),
		time.Now().Add(time.Second),
	)
}

// upload reads binary frames from the client until the configured
// limit or the request context expires. The NDT7 client decides how
// much to send; the mock emits a measurement frame after every
// binary frame so the client can observe per-message byte counts.
func (s *Server) upload(conn *websocket.Conn) {
	var received int64
	for {
		if s.bytesTarget > 0 && received >= s.bytesTarget {
			break
		}
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		received += int64(len(data))
		s.bytesRead.Add(int64(len(data)))
		if err := writeMeasurement(conn, received, received); err != nil {
			return
		}
	}
	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, "mock complete"),
		time.Now().Add(time.Second),
	)
}

// writeMeasurement emits one NDT7 measurement JSON frame with the
// byte counts the mock has observed. The frame carries the
// application-origin AppInfo and a ConnectionInfo populated from the
// upgraded socket.
func writeMeasurement(conn *websocket.Conn, numBytes, elapsed int64) error {
	measurement := struct {
		AppInfo struct {
			NumBytes    int64 `json:"NumBytes"`
			ElapsedTime int64 `json:"ElapsedTime"`
		} `json:"AppInfo"`
		ConnectionInfo struct {
			Client string `json:"Client"`
			Server string `json:"Server"`
		} `json:"ConnectionInfo"`
	}{}
	measurement.AppInfo.NumBytes = numBytes
	measurement.AppInfo.ElapsedTime = elapsed
	measurement.ConnectionInfo.Client = conn.RemoteAddr().String()
	measurement.ConnectionInfo.Server = conn.LocalAddr().String()
	data, err := json.Marshal(measurement)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

// IsRetriableNetError is a small helper exported for tests that need
// to classify the error returned by the provider after the mock drops
// the TCP connection.
func IsRetriableNetError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr)
}
