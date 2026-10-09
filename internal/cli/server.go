package cli

import (
	"context"
	"fmt"

	"github.com/AbsoluteZero001/GoSpeed/internal/server"
	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

func (a *App) runServer(ctx context.Context, args []string) int {
	defaults := server.DefaultConfig()
	set := a.newFlagSet("gospeed server")
	addr := set.String("addr", defaults.Addr, "listen address; keep the loopback default unless you intend to expose the server")
	maxUpload := set.Int64("max-upload-bytes", defaults.MaxUploadBytes, "maximum accepted upload body in bytes")
	maxDownload := set.Int64("max-download-bytes", defaults.MaxDownloadBytes, "maximum download size per request in bytes")
	maxDuration := set.Duration("max-download-duration", defaults.MaxDownloadDuration, "maximum duration of one download response")
	defaultDuration := set.Duration("default-download-duration", defaults.DefaultDownloadDuration, "download duration used when a request sets neither bytes nor duration_ms")
	maxConcurrent := set.Int("max-concurrent-tests", defaults.MaxConcurrentTests, "maximum number of /download and /upload requests served at the same time")
	maxConnections := set.Int("max-connections-per-test", defaults.MaxConnectionsPerTest, "advisory limit advertised through /capabilities for parallel connections in one test")
	readTimeout := set.Duration("read-timeout", defaults.ReadTimeout, "HTTP read timeout")
	writeTimeout := set.Duration("write-timeout", defaults.WriteTimeout, "HTTP write timeout; must exceed the maximum download duration")
	if err := set.Parse(args); err != nil {
		return 2
	}

	cfg := server.DefaultConfig()
	cfg.Addr = *addr
	cfg.MaxUploadBytes = *maxUpload
	cfg.MaxDownloadBytes = *maxDownload
	cfg.MaxDownloadDuration = *maxDuration
	cfg.DefaultDownloadDuration = *defaultDuration
	cfg.MaxConcurrentTests = *maxConcurrent
	cfg.MaxConnectionsPerTest = *maxConnections
	cfg.ReadTimeout = *readTimeout
	cfg.WriteTimeout = *writeTimeout

	srv, err := server.New(cfg)
	if err != nil {
		return a.fail("%v", err)
	}
	if !isLoopbackAddr(srv.Config().Addr) {
		fmt.Fprintf(a.Err, "Warning: %s is not a loopback address; the test endpoints become reachable from the network.\n", srv.Config().Addr)
	}
	listener, err := srv.Listen()
	if err != nil {
		return a.fail("%v", err)
	}
	fmt.Fprintf(a.Out, "GoSpeed %s test server listening on http://%s\n", version.Version, listener.Addr().String())
	fmt.Fprintln(a.Out, "Endpoints: GET /health, GET /ping, GET /download, POST /upload")
	fmt.Fprintln(a.Out, "Press Ctrl+C to stop.")
	if err := srv.Serve(ctx, listener); err != nil {
		return a.fail("%v", err)
	}
	fmt.Fprintln(a.Out, "Server stopped.")
	return 0
}
