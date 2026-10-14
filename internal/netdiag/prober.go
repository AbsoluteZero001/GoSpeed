package netdiag

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

// systemProber is the production Prober.
//
// ICMP goes through the operating system's own ping program rather than a raw
// socket. A raw ICMP socket needs elevated privileges or a driver on Windows,
// which would make the tool unusable for an ordinary user, while ping is
// present on every supported platform and needs no third party component. The
// trade off is that the platform program owns the packet timing; this package
// still owns the classification of its output.
type systemProber struct{}

// ICMP issues echo requests through the platform ping program and parses the
// reported round trips.
func (systemProber) ICMP(ctx context.Context, host string, samples int, timeout time.Duration) (SampleSet, error) {
	if samples <= 0 {
		samples = 1
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	host = hostOf(host)
	if host == "" {
		return SampleSet{Detail: "no host to ping"}, fmt.Errorf("%w: empty host", ErrInvalidOptions)
	}

	runner, ok := platformPing()
	if !ok {
		return SampleSet{Detail: "this platform has no ping program that could be used"},
			fmt.Errorf("%w: ping", ErrToolUnavailable)
	}
	// The platform program gets a total budget derived from the per probe
	// timeout so a cancelled run still returns promptly.
	total := time.Duration(samples)*timeout + timeout
	out, err := runner(ctx, host, samples, timeout, total)
	if err != nil && out == "" {
		return SampleSet{TimedOut: samples, Detail: err.Error()}, err
	}
	return parsePingOutput(out, samples), nil
}

// TCP measures the TCP three way handshake using a fresh connection per sample.
//
// A fresh connection is deliberate: a reused connection measures an already
// established path plus keepalive behaviour, which is not what "TCP connect"
// means to a reader.
func (systemProber) TCP(ctx context.Context, address string, samples int, timeout time.Duration) (SampleSet, error) {
	if samples <= 0 {
		samples = 1
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	set := SampleSet{Samples: make([]time.Duration, 0, samples)}
	dialer := &net.Dialer{Timeout: timeout}
	for index := 0; index < samples; index++ {
		if index > 0 {
			if err := sleepContext(ctx, DefaultInterval); err != nil {
				return set, err
			}
		}
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		conn, err := dialer.DialContext(probeCtx, "tcp", address)
		rtt := time.Since(start)
		cancel()
		if err != nil {
			if isTimeout(err) {
				set.TimedOut++
				continue
			}
			set.Failed++
			if set.Detail == "" {
				set.Detail = err.Error()
			}
			continue
		}
		_ = conn.Close()
		set.Samples = append(set.Samples, rtt)
	}
	if len(set.Samples) == 0 && set.Detail == "" {
		set.Detail = fmt.Sprintf("no connection to %s answered %d probes", address, samples)
	}
	return set, nil
}

// HTTP measures the time from issuing a request to receiving the first response
// byte, the same definition internal/speedtest uses for its own HTTP RTT. Each
// sample uses its own client so the first sample's DNS and TCP setup is not
// silently excluded from the numbers the way it would be on a reused
// connection.
func (systemProber) HTTP(ctx context.Context, url string, samples int, timeout time.Duration) (SampleSet, error) {
	if samples <= 0 {
		samples = 1
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return SampleSet{Detail: "the HTTP probe needs an http:// or https:// URL"},
			fmt.Errorf("%w: http scheme", ErrInvalidOptions)
	}
	set := SampleSet{Samples: make([]time.Duration, 0, samples)}
	for index := 0; index < samples; index++ {
		if index > 0 {
			if err := sleepContext(ctx, DefaultInterval); err != nil {
				return set, err
			}
		}
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		rtt, err := httpProbe(probeCtx, url)
		cancel()
		if err != nil {
			if isTimeout(err) {
				set.TimedOut++
				continue
			}
			set.Failed++
			if set.Detail == "" {
				set.Detail = err.Error()
			}
			continue
		}
		set.Samples = append(set.Samples, rtt)
	}
	if len(set.Samples) == 0 && set.Detail == "" {
		set.Detail = fmt.Sprintf("no HTTP response from %s after %d probes", url, samples)
	}
	return set, nil
}

// httpProbe performs one request and stops at the first response byte.
func httpProbe(ctx context.Context, url string) (time.Duration, error) {
	client := &http.Client{
		Timeout: DefaultTimeout,
		Transport: &http.Transport{
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: DefaultTimeout,
			Proxy:               http.ProxyFromEnvironment,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		// Some servers reject HEAD; fall back to GET so a target that only
		// speaks GET is still measured rather than reported as broken.
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return 0, err
		}
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Cache-Control", "no-store")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	// time.Since reads the monotonic clock, so a wall clock adjustment during
	// the request cannot distort the sample.
	return time.Since(start), nil
}

// systemResolver resolves names with the Go resolver, which honours the
// operating system's resolver configuration including DNS servers learned from
// DHCP, VPN adapters and the hosts file.
type systemResolver struct{}

// LookupHost returns every address the name resolves to.
func (systemResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if host == "" {
		return nil, fmt.Errorf("%w: empty host", ErrInvalidOptions)
	}
	// A literal address resolves to itself, which keeps diagnose usable
	// against a bare IP without a DNS round trip.
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, err
	}
	return addrs, nil
}

// DefaultInterval is the pause between probes of one measurement. It keeps a
// diagnostic from turning into a flood while staying short enough that five
// samples finish quickly.
const DefaultInterval = 100 * time.Millisecond

// sleepContext waits for d unless ctx is cancelled first.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isTimeout reports whether an error is a deadline rather than a refusal or a
// routing failure. Keeping the two apart is what stops "did not answer in
// time" from being reported as "could not be reached".
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}
