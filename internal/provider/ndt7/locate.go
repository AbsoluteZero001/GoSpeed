package ndt7

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	v2 "github.com/m-lab/locate/api/v2"
	"github.com/m-lab/ndt7-client-go"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
)

// Locate v2 discovery.
//
// The discovery path is a consent bound action: a Locate query already
// reveals the client public IP to M-Lab, exactly like a measurement does.
// The consent gate is therefore checked before the request is built, not
// after it is sent.
//
// Why the request is issued here and not through locate.Client.Nearest:
// the official client parses the answer into []v2.Target and discards the
// "next_request" field. The Locate v2 documentation says that clients
// SHOULD NOT make additional requests more often than NextRequest and
// that the server MAY reject requests indefinitely when they do not
// respect that limit, so the field has to be read to be obeyed. The wire
// contract itself is not re-implemented: the URL is built the way the
// official client builds it (the service name joined onto the base path),
// the answer is decoded into the official v2.NearestResult type, and the
// official body size limit is applied.

const (
	// locateService is the Locate v2 service name for NDT7. The service
	// name is joined onto the base URL path, so the effective request URL
	// is <base>/v2/nearest/ndt/ndt7.
	locateService = "ndt/ndt7"

	// DefaultLocateBaseURL is the official M-Lab Locate v2 endpoint.
	DefaultLocateBaseURL = "https://locate.measurementlab.net/v2/nearest/"

	// DefaultLocateTimeout caps one Locate HTTP request. The official
	// client defaults to http.DefaultClient, which carries no timeout at
	// all, so an explicit timeout has to be installed here.
	DefaultLocateTimeout = 10 * time.Second

	// DefaultDiscoveryCacheTTL is the shortest interval between two
	// Locate queries. It exists so that a caller refreshing its node list
	// does not send a query every time, and it is deliberately short: the
	// access_token in a Locate answer is a short lived credential.
	DefaultDiscoveryCacheTTL = 5 * time.Minute

	// trustedLocateScheme is the only scheme an NDT7 measurement URL may
	// use. NDT7 traffic carries the published measurement, so it must be
	// an encrypted WebSocket.
	trustedLocateScheme = "wss"

	// trustedLocatePort is the only port an encrypted measurement URL may
	// name explicitly. A Locate answer on any other port would still
	// pass the hostname check, so the port is pinned rather than trusted.
	trustedLocatePort = "443"

	// tokenExpiryMargin is how much lifetime a cached access_token must
	// still have before the cached discovery may be handed out again.
	tokenExpiryMargin = 60 * time.Second

	// maxLocateURLsPerTarget rejects an implausible Locate answer. A real
	// target carries a handful of service URLs.
	maxLocateURLsPerTarget = 16

	// maxLocateBodyBytes bounds the answer body. The official client
	// applies the same limit.
	maxLocateBodyBytes = 1 << 19
)

// trustedLocateHostSuffixes restricts accepted measurement hosts to the
// two M-Lab operated domains.
//
// "measurement-lab.org" (with a hyphen) is the platform domain that real
// Locate v2 answers use for NDT server hostnames, for example
// "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org". "measurementlab.net"
// (no hyphen) is the M-Lab website domain, kept because the first real
// run proved that a genuine node was rejected when only the website
// domain was listed.
var trustedLocateHostSuffixes = []string{"measurement-lab.org", "measurementlab.net"}

// Errors a discovery may surface. They are sentinels so callers can match
// with errors.Is; the messages are stable for logs and never contain a
// credential.
var (
	// ErrNoLocateServers is returned when Locate answered but listed no
	// server at all.
	ErrNoLocateServers = errors.New("ndt7: Locate returned no servers")
	// ErrNoValidLocateServer is returned when every candidate Locate
	// listed was rejected by the validation gate. The individual reasons
	// are not repeated in the message because they carry host names.
	ErrNoValidLocateServer = errors.New("ndt7: Locate returned no server that passed the trust gate")
	// ErrLocateRateLimited is returned when the cached discovery is no
	// longer usable but the server asked for the next query to be held
	// back. The client would rather report this than ignore the limit.
	ErrLocateRateLimited = errors.New("ndt7: Locate query is rate limited by the server")
	// ErrLocateRedirect is returned when the Locate endpoint answers with
	// a redirect.
	//
	// A redirect is refused on purpose. Following it would send an
	// unauthorised request to a host that was never validated, and the
	// answer would then be trusted as if it had come from M-Lab. The
	// redirect target is deliberately not repeated in the message: the
	// Location header is attacker controlled and the message reaches the
	// user interface.
	ErrLocateRedirect = errors.New("ndt7: the Locate endpoint answered with a redirect; redirects are not followed")
)

// locateResponse is what one Locate v2 query returns.
type locateResponse struct {
	// Targets are the servers the client may measure.
	Targets []v2.Target
	// NextRequest carries the server's scheduling guidance: the earliest
	// time the client should send its next query. It may be nil.
	NextRequest *v2.NextRequest
}

// discoveryCache remembers the most recent successful discovery so that a
// caller refreshing its node list does not send a Locate query every time.
//
// The cache never outlives the access_token it carries, and it never
// sends a query earlier than the server asked for.
type discoveryCache struct {
	nodes []provider.Node
	// expiresAt is when the remembered nodes stop being servable.
	expiresAt time.Time
	// nextRequestAt is the earliest time a new Locate query may be sent.
	nextRequestAt time.Time
}

// cached returns the remembered nodes when they are still usable.
func (c *discoveryCache) cached(now time.Time) ([]provider.Node, bool) {
	if len(c.nodes) == 0 || !now.Before(c.expiresAt) {
		return nil, false
	}
	return c.nodes, true
}

// mayRequest reports whether a new Locate query may be sent now.
func (c *discoveryCache) mayRequest(now time.Time) bool {
	return !now.Before(c.nextRequestAt)
}

// store remembers nodes and the scheduling guidance that came with them.
//
// The nodes stop being servable at the earliest of: the TTL, the
// server's next request time, and the moment the access_token stops
// being usable. A node whose token cannot be read is never cached,
// because there is no way to show that the credential is still valid.
func (c *discoveryCache) store(nodes []provider.Node, now time.Time, ttl time.Duration, next *v2.NextRequest) {
	if len(nodes) == 0 {
		return
	}
	tokenExpiry, ok := earliestTokenExpiry(nodes)
	if !ok {
		// At least one node carries a token whose expiry cannot be
		// read, so the answer is not cached at all.
		return
	}
	if !tokenExpiry.After(now) {
		// The credential is already expired: caching it would hand out a
		// token the server no longer accepts.
		return
	}
	notBefore := now.Add(ttl)
	if next != nil && next.NotBefore.After(notBefore) {
		notBefore = next.NotBefore
	}
	expiresAt := tokenExpiry.Add(-tokenExpiryMargin)
	if notBefore.Before(expiresAt) {
		expiresAt = notBefore
	}
	c.nodes = nodes
	c.expiresAt = expiresAt
	c.nextRequestAt = notBefore
}

// earliestTokenExpiry returns the earliest expiry of the access_tokens
// the nodes carry. The second result is false when a token cannot be
// read.
func earliestTokenExpiry(nodes []provider.Node) (time.Time, bool) {
	var earliest time.Time
	for _, node := range nodes {
		expiry, ok := accessTokenExpiry(node.Endpoint)
		if !ok {
			return time.Time{}, false
		}
		if earliest.IsZero() || expiry.Before(earliest) {
			earliest = expiry
		}
	}
	return earliest, earliest.After(time.Time{})
}

// accessTokenExpiry reads the "exp" claim of the access_token carried by
// a measurement URL.
//
// The token is a JWT, so the claim sits in the base64url encoded payload
// segment. The signature is not verified: the token is issued by M-Lab
// and handed back to M-Lab, and this function only needs to know when the
// credential stops being usable so that it is never cached past its
// lifetime.
func accessTokenExpiry(rawURL string) (time.Time, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return time.Time{}, false
	}
	token := parsed.Query().Get("access_token")
	if token == "" {
		return time.Time{}, false
	}
	segments := strings.Split(token, ".")
	if len(segments) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// locateUserAgent is the User-Agent sent with the Locate query. M-Lab
// rejects requests that do not identify themselves, and the official
// client refuses to send an empty one.
func locateUserAgent() string {
	return ndt7.MakeUserAgent(ClientName, ClientVersion)
}

// queryLocate performs exactly one Locate v2 query.
//
// The request is a single GET and is never retried, so the client can
// not hammer the service. The context governs cancellation and the HTTP
// client carries an explicit timeout, because the official client would
// otherwise use http.DefaultClient, which has none.
func (r *Runner) queryLocate(ctx context.Context) (locateResponse, error) {
	base := r.locateBaseURL
	if base == "" {
		base = DefaultLocateBaseURL
	}
	timeout := r.locateTimeout
	if timeout <= 0 {
		timeout = DefaultLocateTimeout
	}
	// The timeout is applied to the context rather than to the HTTP
	// client, so that it also holds for an injected client.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	parsed, err := url.Parse(base)
	if err != nil {
		return locateResponse{}, fmt.Errorf("ndt7: invalid Locate base URL: %w", err)
	}
	if parsed.Scheme != "https" {
		return locateResponse{}, fmt.Errorf("ndt7: Locate base URL must use https, got %q", parsed.Scheme)
	}
	// The official client joins the service name onto the base path.
	endpoint := *parsed
	endpoint.Path = path.Join(endpoint.Path, locateService)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return locateResponse{}, fmt.Errorf("ndt7: cannot build the Locate request: %w", err)
	}
	request.Header.Set("User-Agent", locateUserAgent())
	// The client is copied rather than used in place, so that the
	// redirect policy below cannot leak into an injected client.
	client := &http.Client{}
	if r.locateHTTPClient != nil {
		*client = *r.locateHTTPClient
	}
	// Refuse redirects. A Locate answer must come from the endpoint the
	// runner was pointed at; following a redirect would send an
	// unauthorised request to a host that was never validated.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return ErrLocateRedirect
	}
	response, err := client.Do(request)
	if err != nil {
		return locateResponse{}, fmt.Errorf("ndt7: Locate query failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxLocateBodyBytes))
	if err != nil {
		return locateResponse{}, fmt.Errorf("ndt7: cannot read the Locate answer: %w", err)
	}
	var result v2.NearestResult
	if err := json.Unmarshal(body, &result); err != nil {
		return locateResponse{}, fmt.Errorf("ndt7: cannot decode the Locate answer: %w", err)
	}
	if result.Error != nil {
		return locateResponse{}, fmt.Errorf("ndt7: Locate reported an error: %s: %s", result.Error.Title, result.Error.Detail)
	}
	if result.Results == nil {
		return locateResponse{}, ErrNoLocateServers
	}
	return locateResponse{Targets: result.Results, NextRequest: result.NextRequest}, nil
}

// discoverTargets performs one Locate query and returns the validated
// targets.
//
// It never returns a partially validated list: a target either passes
// every check or it is dropped.
func (r *Runner) discoverTargets(ctx context.Context) ([]provider.Node, *v2.NextRequest, error) {
	response, err := r.queryLocate(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(response.Targets) == 0 {
		return nil, response.NextRequest, ErrNoLocateServers
	}
	nodes := make([]provider.Node, 0, len(response.Targets))
	rejected := 0
	for index := range response.Targets {
		node, err := nodeFromTarget(&response.Targets[index])
		if err != nil {
			rejected++
			continue
		}
		nodes = append(nodes, node)
	}
	if len(nodes) == 0 {
		return nil, response.NextRequest, fmt.Errorf("%w (%d candidate(s) rejected)", ErrNoValidLocateServer, rejected)
	}
	return nodes, response.NextRequest, nil
}

// nodeFromTarget validates one Locate target and turns it into a
// provider.Node.
//
// The node keeps the COMPLETE service URL that Locate issued, including
// the access_token and every other query parameter, because the M-Lab
// server requires that credential and re-deriving the address from the
// hostname alone would drop it. The token never reaches the node name,
// the location information or any result.
func nodeFromTarget(target *v2.Target) (provider.Node, error) {
	if target == nil {
		return provider.Node{}, errors.New("Locate target is nil")
	}
	hostname := strings.TrimSpace(target.Hostname)
	if hostname == "" {
		return provider.Node{}, errors.New("Locate target has no hostname")
	}
	if len(target.URLs) == 0 {
		return provider.Node{}, fmt.Errorf("Locate target %q has no measurement URLs", hostname)
	}
	if len(target.URLs) > maxLocateURLsPerTarget {
		return provider.Node{}, fmt.Errorf("Locate target %q exposes an implausible number of URLs (%d)", hostname, len(target.URLs))
	}
	download, err := validatedServiceURL(target, ndt7Path(DirectionDownload))
	if err != nil {
		return provider.Node{}, err
	}
	if _, err := validatedServiceURL(target, ndt7Path(DirectionUpload)); err != nil {
		return provider.Node{}, err
	}
	return provider.Node{
		ID:        hostname,
		Name:      nodeDisplayName(hostname, target.Location),
		Endpoint:  download,
		Scope:     provider.ScopeRemote,
		Provider:  provider.IDMLabNDT7,
		Ownership: provider.OwnershipThirdParty,
		Discovery: provider.DiscoveryService,
	}, nil
}

// nodeDisplayName keeps the human readable part of a target: its
// hostname plus the location Locate reported, when there is one. It never
// contains a URL and therefore never contains a credential.
func nodeDisplayName(hostname string, location *v2.Location) string {
	if location == nil {
		return hostname
	}
	city := strings.TrimSpace(location.City)
	country := strings.TrimSpace(location.Country)
	switch {
	case city != "" && country != "":
		return fmt.Sprintf("%s (%s, %s)", hostname, city, country)
	case city != "":
		return fmt.Sprintf("%s (%s)", hostname, city)
	case country != "":
		return fmt.Sprintf("%s (%s)", hostname, country)
	default:
		return hostname
	}
}

// validatedServiceURL returns the complete service URL of one direction
// after it passed the trust gate.
//
// The official SDK looks a target's URL up under the key
// "<scheme>://<path>", which for NDT7 is the triple slash key
// "wss:///ndt/v7/download". A target that does not carry the key for
// this direction is unusable for it.
func validatedServiceURL(target *v2.Target, path string) (string, error) {
	raw, ok := target.URLs[trustedLocateScheme+"://"+path]
	if !ok || strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("Locate target %q has no usable %s URL", target.Hostname, path)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("Locate target %q has an unparseable %s URL", target.Hostname, path)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("Locate target %q embeds credentials in its %s URL", target.Hostname, path)
	}
	if parsed.Scheme != trustedLocateScheme {
		return "", fmt.Errorf("Locate target %q serves %q over insecure scheme %q", target.Hostname, path, parsed.Scheme)
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("Locate target %q carries a fragment in its %s URL", target.Hostname, path)
	}
	if port := parsed.Port(); port != "" && port != trustedLocatePort {
		return "", fmt.Errorf("Locate target %q serves %s over nonstandard port %q", target.Hostname, path, port)
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("Locate target %q has an empty host in its %s URL", target.Hostname, path)
	}
	if !hostAllowed(host, trustedLocateHostSuffixes) {
		return "", fmt.Errorf("Locate target %q host %q is not in the trusted list", target.Hostname, path)
	}
	if parsed.Path != path {
		return "", fmt.Errorf("Locate target %q serves %q on an unexpected path", target.Hostname, path)
	}
	return raw, nil
}

// hostAllowed reports whether host belongs to one of the trusted suffixes.
//
// Matching is case insensitive and tolerates a single trailing dot (the
// canonical FQDN form), but enforces full DNS label boundaries: "evil" +
// "measurement-lab.org" does not match "measurement-lab.org".
//
// After normalisation the host must consist solely of DNS name
// characters. A host carrying control characters, Unicode look alikes or
// any other byte can never match, so a crafted name cannot slip through
// normalisation into a suffix match. IP literals do not pass either, which
// is intended: a literal address must never satisfy a trusted hostname
// suffix.
func hostAllowed(host string, suffixes []string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if !dnsNameRunes(host) {
		return false
	}
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

// dnsNameRunes reports whether every rune of host is a lowercase DNS name
// character: a letter, a digit, a dot or a hyphen.
func dnsNameRunes(host string) bool {
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '-':
		default:
			return false
		}
	}
	return host != ""
}
