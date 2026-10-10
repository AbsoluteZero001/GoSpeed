package mlabpoc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/m-lab/locate/api/locate"
	v2 "github.com/m-lab/locate/api/v2"
	"github.com/m-lab/ndt7-client-go"
)

// Locate service name and the URL path keys used by the official NDT7 SDK.
// The SDK builds its URL lookup key as `Scheme + "://" + path`, which yields
// the triple-slash keys below (see ndt7-client-go ndt7.go
// nextURLFromLocate). DiscoverServers validates that a target carries these
// exact keys before any connection is attempted.
const (
	locateService = "ndt/ndt7"

	// DefaultLocateBaseURL is the official M-Lab Locate v2 endpoint.
	DefaultLocateBaseURL = "https://locate.measurementlab.net/v2/nearest/"

	ndt7DownloadPath = "/ndt/v7/download"
	ndt7UploadPath   = "/ndt/v7/upload"
)

// DefaultLocateHostSuffixes restricts validated targets to M-Lab operated
// hostnames. Local mock tests override this list.
//
// "measurement-lab.org" (with a hyphen) is the platform domain that real
// Locate v2 results use for NDT server hostnames, e.g.
// "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org". "measurementlab.net"
// (no hyphen) is the M-Lab website domain kept for older deployments; the
// first real run proved the gate correctly rejected a genuine node because
// only the website domain was listed.
var DefaultLocateHostSuffixes = []string{"measurement-lab.org", "measurementlab.net"}

// DefaultLocateSchemes is the production scheme allow list: NDT7 measurement
// URLs must always be encrypted WebSocket endpoints.
var DefaultLocateSchemes = []string{"wss"}

// LocateOptions configures DiscoverServers. The zero value selects the
// official Locate endpoint, a DefaultLocateTimeout HTTP client timeout and
// the strict production validation (wss + M-Lab operated host suffixes).
type LocateOptions struct {
	// BaseURL overrides the Locate endpoint (for localhost mocks).
	BaseURL string

	// Timeout applies to the whole Locate HTTP request (headers + body).
	// 0 selects DefaultLocateTimeout.
	Timeout time.Duration

	// Authorization is the optional bearer token for client-integration
	// registration.
	Authorization string

	// HTTPClient replaces the default client entirely (tests inject failing
	// DNS transports here). When nil a client with Timeout is used.
	HTTPClient *http.Client

	// SchemeAllowList limits accepted measurement URL schemes.
	// nil selects DefaultLocateSchemes.
	SchemeAllowList []string

	// HostSuffixes limits accepted target hosts. nil selects
	// DefaultLocateHostSuffixes.
	HostSuffixes []string

	ClientName    string
	ClientVersion string
}

// DiscoverServers performs one Locate v2 Nearest query and returns validated
// targets only. It is the ONLY place in this experiment where the official
// Locate client is used.
//
// Safety properties:
//
//   - Consent gate: without an agreed ConsentRecord it returns an error
//     before creating any HTTP request (zero network activity).
//   - HTTP timeout: the default HTTPClient carries an explicit Timeout
//     because locate.NewClient defaults to http.DefaultClient (no timeout).
//   - Context: the context is handed to the official client, which passes it
//     into http.NewRequestWithContext.
//   - Validation: every target must carry usable, scheme-allowed, host-
//     allowed measurement URLs; targets failing validation abort discovery.
func DiscoverServers(ctx context.Context, consent *ConsentRecord, opts LocateOptions) ([]v2.Target, error) {
	if !consentAgreed(consent) {
		return nil, newClassifiedError(ErrorConsent,
			errors.New("Locate discovery requires prior privacy consent; no request was sent"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultLocateTimeout
	}
	schemes := opts.SchemeAllowList
	if schemes == nil {
		schemes = DefaultLocateSchemes
	}
	suffixes := opts.HostSuffixes
	if suffixes == nil {
		suffixes = DefaultLocateHostSuffixes
	}
	clientName, clientVersion := opts.ClientName, opts.ClientVersion
	if clientName == "" {
		clientName = defaultClientName
	}
	if clientVersion == "" {
		clientVersion = defaultClientVersion
	}

	client, err := newLocateClient(opts, clientName, clientVersion)
	if err != nil {
		return nil, newClassifiedError(ErrorConfiguration, err)
	}
	targets, err := client.Nearest(ctx, locateService)
	if err != nil {
		return nil, classifyLocateError(err)
	}
	// The official client returns an empty slice without an error when the
	// reply contains "results": [] (audited in P0-K). Treat an empty list as
	// "no servers" so callers never fall through to a zero-target run.
	if len(targets) == 0 {
		return nil, newClassifiedError(ErrorNoServer, errors.New("Locate returned no usable servers"))
	}
	for i := range targets {
		if err := ValidateTarget(&targets[i], schemes, suffixes); err != nil {
			return nil, newClassifiedError(ErrorServerInvalid, err)
		}
	}
	return targets, nil
}

// newLocateClient builds an official locate.Client with safe overrides.
func newLocateClient(opts LocateOptions, clientName, clientVersion string) (*locate.Client, error) {
	baseURL := DefaultLocateBaseURL
	if opts.BaseURL != "" {
		baseURL = opts.BaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Locate base URL %q: %w", baseURL, err)
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: opts.Timeout}
	}
	client := locate.NewClient(ndt7.MakeUserAgent(clientName, clientVersion))
	client.BaseURL = parsed
	client.HTTPClient = httpClient
	client.Authorization = opts.Authorization
	return client, nil
}

// classifyLocateError maps official client errors onto ErrorClass values.
// The audited official client returns raw transport errors (wrapped in
// *url.Error), locate.ErrNoAvailableServers, JSON decode errors, or a
// formatted "Title: Detail" error for non-200 replies carrying an RFC 7807
// problem document.
func classifyLocateError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return newClassifiedError(ErrorCancelled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return newClassifiedError(ErrorTimeout, err)
	case errors.Is(err, locate.ErrNoAvailableServers):
		return newClassifiedError(ErrorNoServer, err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// http.Client.Timeout surfaces as "context deadline exceeded
		// (Client.Timeout exceeded while awaiting headers)".
		if strings.Contains(err.Error(), "Client.Timeout") {
			return newClassifiedError(ErrorTimeout, err)
		}
		var dnsErr *net.DNSError
		var netErr net.Error
		switch {
		case errors.As(err, &dnsErr):
			return newClassifiedError(ErrorNetwork, err)
		case errors.As(err, &netErr) && netErr.Timeout():
			return newClassifiedError(ErrorTimeout, err)
		default:
			return newClassifiedError(ErrorNetwork, err)
		}
	}
	return newClassifiedError(ErrorUnknown, err)
}

// ValidateTarget checks one Locate target. A target is usable only when it
// carries a syntactically valid download and upload measurement URL whose
// scheme is in the allow list and whose host matches one of the allowed
// suffixes. This is the P0-K-C requirement that nodes must come from a
// trusted Locate result AND pass safety validation.
func ValidateTarget(target *v2.Target, schemes, hostSuffixes []string) error {
	if target == nil {
		return errors.New("Locate target is nil")
	}
	if strings.TrimSpace(target.Hostname) == "" {
		return fmt.Errorf("Locate target %q has no hostname", target.Machine)
	}
	if len(target.URLs) == 0 {
		return fmt.Errorf("Locate target %q has no measurement URLs", target.Hostname)
	}
	if len(target.URLs) > 16 {
		return fmt.Errorf("Locate target %q exposes an implausible number of URLs (%d)", target.Hostname, len(target.URLs))
	}
	for _, path := range []string{ndt7DownloadPath, ndt7UploadPath} {
		if err := validateTargetURL(target, path, schemes, hostSuffixes); err != nil {
			return err
		}
	}
	return nil
}

func validateTargetURL(target *v2.Target, path string, schemes, hostSuffixes []string) error {
	// The official SDK looks up "scheme:///ndt/v7/..."; the key must match
	// for at least one allowed scheme, otherwise the SDK would fall back to
	// an empty URL.
	var rawURL string
	var keyScheme string
	for _, scheme := range schemes {
		key := scheme + "://" + path
		if candidate, ok := target.URLs[key]; ok && strings.TrimSpace(candidate) != "" {
			rawURL = candidate
			keyScheme = scheme
			break
		}
	}
	if rawURL == "" {
		return fmt.Errorf("Locate target %q has no usable %s URL (keys: %v)", target.Hostname, path, keysOf(target.URLs))
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("Locate target %q has an unparseable %s URL: %w", target.Hostname, path, err)
	}
	if parsed.User != nil {
		return fmt.Errorf("Locate target %q embeds credentials in its %s URL", target.Hostname, path)
	}
	if parsed.Scheme != keyScheme {
		return fmt.Errorf("Locate target %q serves %q over insecure scheme %q", target.Hostname, path, parsed.Scheme)
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("Locate target %q has an empty host in its %s URL", target.Hostname, path)
	}
	if !hostAllowed(host, hostSuffixes) {
		return fmt.Errorf("Locate target %q host %q is not in the trusted list %v", target.Hostname, host, hostSuffixes)
	}
	return nil
}

func hostAllowed(host string, suffixes []string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	for _, suffix := range suffixes {
		suffix = strings.ToLower(strings.TrimSpace(suffix))
		if suffix == "" {
			continue
		}
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

// TargetServiceURL returns the validated service URL for a direction from a
// validated target. Callers must have validated the target before.
func TargetServiceURL(target *v2.Target, scheme string, direction Direction) (string, error) {
	var path string
	switch direction {
	case DirectionDownload:
		path = ndt7DownloadPath
	case DirectionUpload:
		path = ndt7UploadPath
	default:
		return "", fmt.Errorf("unsupported direction %q", direction)
	}
	raw, ok := target.URLs[scheme+"://"+path]
	if !ok || strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("target has no %s URL for scheme %q", path, scheme)
	}
	return raw, nil
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
