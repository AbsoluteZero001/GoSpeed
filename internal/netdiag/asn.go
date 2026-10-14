package netdiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RIPEstat endpoints used by the default enricher. Both are documented public
// services that answer over plain HTTPS with no key.
const (
	ripeatatBase        = "https://stat.ripe.net"
	ripeatatNetworkInfo = "/data/network-info/data.json"
	ripeatatASOverview  = "/data/as-overview/data.json"
)

// ASNSource names the provider an attribution came from.
const ASNSource = "ripeatat"

// asnCacheTTL is how long an answer is reused.
//
// Enrichment is a convenience, not a measurement. Caching keeps repeated
// diagnose runs from hammering a third party, and a short TTL means an address
// that changed hands is not reported from a stale answer for long.
const asnCacheTTL = time.Hour

// asnMinInterval is the minimum gap between outbound requests.
//
// RIPEstat publishes a usage policy that asks automated clients to stay well
// below one request per second. The limiter is per instance, and the CLI builds
// one per run.
const asnMinInterval = 350 * time.Millisecond

// asnTimeout bounds a single request so a slow third party cannot extend a
// diagnosis that is otherwise complete.
const asnTimeout = 5 * time.Second

// ErrEnrichmentUnavailable reports that an enrichment source could not answer.
//
// It is deliberately its own error so a caller can tell "the attribution is
// missing" apart from "a measurement failed". An enrichment failure must never
// change or remove a measurement, so Diagnose records it and carries on.
var ErrEnrichmentUnavailable = errors.New("netdiag: ASN enrichment is unavailable")

// RIPEstatEnricher looks up address ownership through the RIPEstat data API.
//
// It is the default Enricher. It is constructed explicitly rather than being a
// package global so its cache and rate limiter are owned by one caller, and so
// tests can supply their own.
type RIPEstatEnricher struct {
	client *http.Client

	// base overrides the service root. Empty means the public RIPEstat service.
	// It exists so a test can point the enricher at a local server; nothing in
	// production ever sets it.
	base string

	mu        sync.Mutex
	cache     map[string]*ASInfo
	expiresAt map[string]time.Time
	lastCall  time.Time
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
}

// endpoint joins the configured root with a service path.
func (r *RIPEstatEnricher) endpoint(path string) string {
	if r.base != "" {
		return r.base + path
	}
	return ripeatatBase + path
}

// NewRIPEstatEnricher returns an enricher using a bounded HTTP client.
func NewRIPEstatEnricher() *RIPEstatEnricher {
	return &RIPEstatEnricher{
		client: &http.Client{
			Timeout: asnTimeout,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				MaxIdleConnsPerHost: 2,
			},
		},
		cache:     make(map[string]*ASInfo),
		expiresAt: make(map[string]time.Time),
		now:       time.Now,
		sleep:     sleepContext,
	}
}

// Enrich returns ownership information for the given addresses.
//
// It never returns an error for an individual address that could not be looked
// up: that address gets an ASInfo with a Detail instead, so the caller can show
// which parts are known and which are not. A transport level failure returns an
// error, which Diagnose records without touching the measurements.
func (r *RIPEstatEnricher) Enrich(ctx context.Context, addresses []string) ([]*ASInfo, error) {
	results := make([]*ASInfo, 0, len(addresses))
	var firstFailure error

	for _, address := range publicAddresses(addresses) {
		if cached, ok := r.cached(address); ok {
			results = append(results, cached)
			continue
		}
		info, err := r.lookup(ctx, address)
		if err != nil {
			if firstFailure == nil {
				firstFailure = err
			}
			// One failed address does not stop the others.
			results = append(results, &ASInfo{
				Address: address,
				Holder:  Unknown,
				Detail:  "lookup failed: " + err.Error(),
			})
			continue
		}
		r.remember(address, info)
		results = append(results, info)
	}
	// A cancelled context is reported even when some answers were already
	// collected: the caller asked to stop, so it must not be handed a complete
	// looking answer set.
	if ctx.Err() != nil {
		return results, ctx.Err()
	}
	if len(results) == 0 && firstFailure != nil {
		return nil, firstFailure
	}
	return results, nil
}

// cached returns a cached answer that has not expired.
func (r *RIPEstatEnricher) cached(address string) (*ASInfo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	info, ok := r.cache[address]
	if !ok {
		return nil, false
	}
	if r.now().After(r.expiresAt[address]) {
		return nil, false
	}
	copied := *info
	return &copied, true
}

// remember stores an answer with its expiry.
func (r *RIPEstatEnricher) remember(address string, info *ASInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copied := *info
	r.cache[address] = &copied
	r.expiresAt[address] = r.now().Add(asnCacheTTL)
}

// lookup resolves one address through network-info and then as-overview.
func (r *RIPEstatEnricher) lookup(ctx context.Context, address string) (*ASInfo, error) {
	prefix, asn, err := r.networkInfo(ctx, address)
	if err != nil {
		return nil, err
	}
	holder, err := r.holder(ctx, asn)
	if err != nil {
		// The AS number is the more useful half; a failed organisation lookup
		// degrades that one field instead of discarding the answer.
		return &ASInfo{
			Address: address, ASN: asn, Holder: Unknown, Prefix: prefix,
			Source: ASNSource,
			Detail: "the autonomous system number was resolved but the holder name lookup failed: " + err.Error(),
		}, nil
	}
	return &ASInfo{
		Address: address, ASN: asn, Holder: holder, Prefix: prefix,
		Source: ASNSource,
		Detail: "this names who registers the address; it is not evidence of the path a packet took",
	}, nil
}

// networkInfo asks for the prefix and origin AS of an address.
func (r *RIPEstatEnricher) networkInfo(ctx context.Context, address string) (prefix string, asn uint32, err error) {
	payload := struct {
		Prefix string `json:"prefix"`
		Asns   []int  `json:"asns"`
	}{}
	if err := r.get(ctx, r.endpoint(ripeatatNetworkInfo)+"?resource="+address, &payload); err != nil {
		return "", 0, err
	}
	if len(payload.Asns) == 0 {
		return payload.Prefix, 0, nil
	}
	if payload.Asns[0] < 0 {
		return payload.Prefix, 0, nil
	}
	return payload.Prefix, uint32(payload.Asns[0]), nil
}

// holder asks for the organisation that registered an autonomous system.
func (r *RIPEstatEnricher) holder(ctx context.Context, asn uint32) (string, error) {
	if asn == 0 {
		return Unknown, nil
	}
	payload := struct {
		Holder string `json:"holder"`
	}{}
	if err := r.get(ctx, r.endpoint(ripeatatASOverview)+"?resource=AS"+strconv.FormatUint(uint64(asn), 10), &payload); err != nil {
		return Unknown, err
	}
	if payload.Holder == "" {
		return Unknown, nil
	}
	return payload.Holder, nil
}

// get performs one rate limited, cached-by-the-caller request.
func (r *RIPEstatEnricher) get(ctx context.Context, url string, out any) error {
	if err := r.waitForTurn(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	// A descriptive User-Agent is part of being a good citizen when a service
	// is queried automatically.
	req.Header.Set("User-Agent", "GoSpeed/"+asnUserAgentVersion)

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s answered %s", ErrEnrichmentUnavailable, url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: %s returned malformed json", ErrEnrichmentUnavailable, url)
	}
	return nil
}

// waitForTurn enforces the minimum interval between outbound requests.
func (r *RIPEstatEnricher) waitForTurn(ctx context.Context) error {
	r.mu.Lock()
	now := r.now()
	wait := r.lastCall.Add(asnMinInterval).Sub(now)
	if wait <= 0 {
		r.lastCall = now
		r.mu.Unlock()
		return nil
	}
	r.lastCall = r.lastCall.Add(asnMinInterval)
	r.mu.Unlock()
	return r.sleep(ctx, wait)
}

// asnUserAgentVersion identifies the client to the data provider.
const asnUserAgentVersion = "0.6.0-net01"

// StaticEnricher returns fixed answers. It exists so a caller without network
// access can still exercise the enrichment path deterministically.
type StaticEnricher struct {
	Answers map[string]ASInfo
	Err     error
}

// Enrich returns the configured answers, or Err when one is configured.
func (s StaticEnricher) Enrich(ctx context.Context, addresses []string) ([]*ASInfo, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	out := make([]*ASInfo, 0, len(addresses))
	for _, address := range addresses {
		if answer, ok := s.Answers[address]; ok {
			copied := answer
			out = append(out, &copied)
			continue
		}
		out = append(out, &ASInfo{Address: address, Holder: Unknown, Detail: "no answer is configured for this address"})
	}
	return out, nil
}
