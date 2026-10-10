package mock

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/m-lab/ndt7-client-go/spec"
	"github.com/m-lab/tcp-info/tcp"
)

const subprotocol = "net.measurementlab.ndt.v7"

type Scenario string

const (
	ScenarioNormalDownload    Scenario = "normal_download"
	ScenarioNormalUpload      Scenario = "normal_upload"
	ScenarioHandshakeFailure  Scenario = "handshake_failure"
	ScenarioEarlyClose        Scenario = "early_close"
	ScenarioNetworkDisconnect Scenario = "network_disconnect"
	ScenarioStall             Scenario = "stall"
)

type Snapshot struct {
	Requests    int64 `json:"requests"`
	Connections int64 `json:"connections"`
	Active      int64 `json:"active"`
}

type Server struct {
	scenario Scenario
	server   *httptest.Server
	stopOnce sync.Once
	stop     chan struct{}

	requests    atomic.Int64
	connections atomic.Int64
	active      atomic.Int64
}

func New(scenario Scenario) *Server {
	s := &Server{scenario: scenario, stop: make(chan struct{})}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *Server) Close() {
	s.stopOnce.Do(func() { close(s.stop) })
	s.server.Close()
}

func (s *Server) Host() string {
	return s.server.Listener.Addr().String()
}

func (s *Server) URL() string {
	return s.server.URL
}

func (s *Server) Snapshot() Snapshot {
	return Snapshot{
		Requests:    s.requests.Load(),
		Connections: s.connections.Load(),
		Active:      s.active.Load(),
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	if s.scenario == ScenarioHandshakeFailure {
		http.Error(w, "mock handshake rejected", http.StatusForbidden)
		return
	}
	upgrader := websocket.Upgrader{
		Subprotocols: []string{subprotocol},
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
	case ScenarioNormalDownload:
		s.download(conn, true)
	case ScenarioNormalUpload:
		s.upload(conn, true)
	case ScenarioEarlyClose:
		_ = conn.WriteMessage(websocket.BinaryMessage, make([]byte, 8*1024))
	case ScenarioNetworkDisconnect:
		_ = conn.UnderlyingConn().Close()
	case ScenarioStall:
		select {
		case <-s.stop:
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
	default:
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseProtocolError, "unknown scenario"), time.Now().Add(time.Second))
	}
}

func (s *Server) download(conn *websocket.Conn, final bool) {
	frame := make([]byte, 16*1024)
	var total int64
	for i := 0; i < 16; i++ {
		if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
			return
		}
		total += int64(len(frame))
		if err := writeMeasurement(conn, spec.TestDownload, total, total, 12_000, 1_500); err != nil {
			return
		}
	}
	if final {
		_ = writeMeasurement(conn, spec.TestDownload, total, total, 12_000, 1_500)
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "mock complete"), time.Now().Add(time.Second))
}

func (s *Server) upload(conn *websocket.Conn, final bool) {
	var received int64
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if messageType != websocket.BinaryMessage {
			continue
		}
		received += int64(len(data))
		if err := writeMeasurement(conn, spec.TestUpload, received, received, 9_000, 1_000); err != nil {
			return
		}
		if received >= 512*1024 {
			break
		}
	}
	if final {
		_ = writeMeasurement(conn, spec.TestUpload, received, received, 9_000, 1_000)
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "mock complete"), time.Now().Add(time.Second))
}

func writeMeasurement(conn *websocket.Conn, test spec.TestKind, bytes, elapsed int64, minRTT, rttVar uint32) error {
	measurement := spec.Measurement{
		AppInfo: &spec.AppInfo{
			NumBytes:    bytes,
			ElapsedTime: elapsed,
		},
		ConnectionInfo: &spec.ConnectionInfo{
			Client: conn.RemoteAddr().String(),
			Server: conn.LocalAddr().String(),
			UUID:   "mock-uuid",
		},
		Origin: spec.OriginServer,
		Test:   test,
		TCPInfo: &spec.TCPInfo{
			LinuxTCPInfo: tcp.LinuxTCPInfo{
				BytesSent:     bytes,
				BytesReceived: bytes,
				MinRTT:        minRTT,
				RTTVar:        rttVar,
			},
			ElapsedTime: elapsed,
		},
	}
	data, err := json.Marshal(measurement)
	if err != nil {
		return err
	}
	return conn.WriteMessage(websocket.TextMessage, data)
}

func (s Scenario) String() string {
	return string(s)
}
