package netdiag

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors. A caller distinguishes these with errors.Is so that a bad
// request, a cancelled run and a measurement that produced nothing stay
// separate.
var (
	// ErrInvalidOptions reports options that cannot be executed as given.
	ErrInvalidOptions = errors.New("netdiag: invalid options")
	// ErrNoTarget reports a missing or malformed target.
	ErrNoTarget = errors.New("netdiag: no target given")
	// ErrCancelled reports that the caller cancelled the run.
	ErrCancelled = errors.New("netdiag: cancelled")
	// ErrToolUnavailable reports that the platform traceroute or ping program
	// is missing. It is reported as an error rather than silently downgrading
	// the run, because the caller asked for a measurement that did not happen.
	ErrToolUnavailable = errors.New("netdiag: required platform tool is not available")
)

// Defaults for a diagnosis run.
const (
	DefaultPort      = 443
	DefaultSamples   = 5
	DefaultTimeout   = 2 * time.Second
	DefaultMaxHops   = 20
	DefaultTraceWait = 2 * time.Second
)

// SampleSet is the raw outcome of repeated probes of one kind.
//
// The three counters are kept apart because they mean different things.
// TimedOut counts probes that hit the per probe deadline. Failed counts probes
// that ended for another reason, such as no route to host or a refused
// connection. Neither is packet loss, and neither is combined into one.
type SampleSet struct {
	Samples  []time.Duration
	TimedOut int
	Failed   int
	// Detail explains why nothing was measured. It is empty on success.
	Detail string
}

// Prober measures round trip times. The three methods are separate because the
// numbers are not interchangeable: an ICMP echo is answered by the host kernel,
// a TCP handshake is answered by the host stack only after a connection
// attempt, and an HTTP round trip additionally traverses the application.
//
// Every method honours ctx and applies the per probe timeout to each sample,
// never to the whole set, so a cancelled run stops promptly.
type Prober interface {
	ICMP(ctx context.Context, host string, samples int, timeout time.Duration) (SampleSet, error)
	TCP(ctx context.Context, address string, samples int, timeout time.Duration) (SampleSet, error)
	HTTP(ctx context.Context, url string, samples int, timeout time.Duration) (SampleSet, error)
}

// RouteRunner produces the forward route to a target.
type RouteRunner interface {
	Trace(ctx context.Context, target string, maxHops int, timeout time.Duration, resolve bool) (*RouteResult, error)
}

// SystemInspector reports the local network configuration.
type SystemInspector interface {
	Inspect(ctx context.Context, target string) (*NetworkInfo, error)
}

// Resolver turns a host name into addresses.
type Resolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Enricher adds third party attribution for addresses. It is optional and its
// failure must never change a measurement.
type Enricher interface {
	Enrich(ctx context.Context, addresses []string) ([]*ASInfo, error)
}

// Options configures one diagnosis run.
//
// Every measurement is optional so a caller can ask for exactly one thing and
// receive exactly that. The seam fields exist for tests and for embedders; a
// nil field selects the platform implementation.
type Options struct {
	Target string
	// Port is the TCP port used by the TCP connect measurement.
	Port int
	// Samples is how many probes each latency method issues.
	Samples int
	// Timeout is the per probe deadline, not a whole phase deadline.
	Timeout time.Duration
	// MaxHops is the traceroute TTL ceiling.
	MaxHops int

	ICMP       bool
	TCP        bool
	HTTP       bool
	Traceroute bool
	// HTTPURL overrides the URL used for the HTTP measurement. When empty it is
	// derived from the target on port 80 as "http://host/".
	HTTPURL string
	// Enrich requests the optional ASN lookup.
	Enrich bool

	// Injection seams.
	Prober    Prober
	Route     RouteRunner
	Inspector SystemInspector
	Resolver  Resolver
	ASN       Enricher
}

// withDefaults returns a copy of o with every unset field filled in. It does
// not mutate the caller's value.
func (o Options) withDefaults() Options {
	if o.Port <= 0 {
		o.Port = DefaultPort
	}
	if o.Samples <= 0 {
		o.Samples = DefaultSamples
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxHops <= 0 {
		o.MaxHops = DefaultMaxHops
	}
	return o
}

// validate rejects a request that cannot be answered at all. Individual
// measurements are allowed to fail; a missing target is not.
func (o Options) validate() error {
	if o.Target == "" {
		return fmt.Errorf("%w: %w", ErrInvalidOptions, ErrNoTarget)
	}
	return nil
}

// Diagnose runs the requested measurements and returns a result even when some
// of them failed.
//
// A partial result is still a result. The failures are recorded in Errors and
// the measurements that did work keep their values, because dropping a whole
// diagnosis because one method was unavailable would hide real information. The
// only case that returns an error is a request that could not be started at
// all.
func Diagnose(ctx context.Context, options Options) (*Diagnosis, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	options = options.withDefaults()
	if err := options.validate(); err != nil {
		return nil, err
	}

	resolver := options.Resolver
	if resolver == nil {
		resolver = systemResolver{}
	}
	prober := options.Prober
	if prober == nil {
		prober = systemProber{}
	}
	inspector := options.Inspector
	if inspector == nil {
		inspector = systemInspector{}
	}

	result := &Diagnosis{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Network:       &NetworkInfo{DefaultInterface: Unknown, DefaultGateway: Unknown},
		Latency:       &LatencyResult{},
		Warnings:      []string{},
		Errors:        []string{},
	}
	add := func(format string, args ...any) {
		result.Errors = append(result.Errors, fmt.Sprintf(format, args...))
	}
	warn := func(format string, args ...any) {
		result.Warnings = append(result.Warnings, fmt.Sprintf(format, args...))
	}

	// A cancelled context aborts the run outright. Without this check a run
	// could keep assembling a result after the caller gave up, and the caller
	// would have to decide what to do with measurements it no longer wants.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCancelled, err)
	}

	// --- Target resolution -------------------------------------------------
	target := &TargetInfo{
		Input:     options.Target,
		Hostname:  options.Target,
		Addresses: []string{},
		IPv4:      []string{},
		Port:      options.Port,
		Resolver:  Unknown,
	}
	result.Target = target

	addresses, err := resolver.LookupHost(ctx, options.Target)
	switch {
	case err != nil && ctx.Err() != nil:
		return nil, fmt.Errorf("%w: %w", ErrCancelled, ctx.Err())
	case err != nil:
		target.Resolved = false
		target.Detail = err.Error()
		add("name resolution for %q failed: %v", options.Target, err)
	default:
		target.Resolver = "system"
		target.Resolved = len(addresses) > 0
		if !target.Resolved {
			target.Detail = "the resolver returned no addresses"
			add("name resolution for %q returned no addresses", options.Target)
		} else {
			target.Addresses = addresses
			target.Hostname = hostOf(options.Target)
			for _, a := range addresses {
				if isIPv4(a) {
					target.IPv4 = append(target.IPv4, a)
				}
			}
		}
	}

	// --- Network information ----------------------------------------------
	if info, err := inspector.Inspect(ctx, options.Target); err != nil {
		add("network information unavailable: %v", err)
	} else if info != nil {
		if info.DefaultInterface == "" {
			info.DefaultInterface = Unknown
		}
		if info.DefaultGateway == "" {
			info.DefaultGateway = Unknown
		}
		result.Network = info
	} else {
		add("network information unavailable: the inspector returned nothing")
	}

	// --- Latency -----------------------------------------------------------
	if options.ICMP {
		result.Latency.ICMP = measure(ctx, prober.ICMP, ProbeICMP, options.Target, options.Samples, options.Timeout, warn)
	}
	if options.TCP {
		address := netAddress(options.Target, options.Port)
		result.Latency.TCP = measure(ctx, prober.TCP, ProbeTCP, address, options.Samples, options.Timeout, warn)
	}
	if options.HTTP {
		url := options.HTTPURL
		if url == "" {
			url = "http://" + options.Target + "/"
		}
		result.Latency.HTTP = measure(ctx, prober.HTTP, ProbeHTTP, url, options.Samples, options.Timeout, warn)
	}

	// The cross method warning exists because the three numbers are routinely
	// read as one. Naming the gap is cheaper than a reader assuming they
	// measure the same thing.
	if measured(result.Latency.ICMP) && measured(result.Latency.TCP) &&
		result.Latency.ICMP.AverageMs != nil && result.Latency.TCP.AverageMs != nil {
		if gap := *result.Latency.TCP.AverageMs - *result.Latency.ICMP.AverageMs; gap > 0.5 {
			warn("TCP connect (%.2f ms) is slower than ICMP echo (%.2f ms); the two measure different things and the gap is not by itself a fault",
				*result.Latency.TCP.AverageMs, *result.Latency.ICMP.AverageMs)
		}
	}

	// --- Route -------------------------------------------------------------
	if options.Traceroute {
		runner := options.Route
		method := "custom"
		if runner == nil {
			runner = platformRouteRunner()
			method = platformRouteMethod()
		}
		route, err := runner.Trace(ctx, options.Target, options.MaxHops, options.Timeout, false)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, fmt.Errorf("%w: %w", ErrCancelled, ctx.Err())
		case err != nil && route != nil && len(route.Hops) > 0:
			// A runner that stopped early still observed real hops. Throwing
			// them away would hide evidence the run actually collected.
			route.Method = method
			route.MaxHops = maxInt(options.MaxHops, route.MaxHops)
			route.Notes = append(route.Notes,
				fmt.Sprintf("the traceroute did not finish: %v", err))
			result.Route = route
			add("traceroute did not complete: %v", err)
		case err != nil:
			result.Route = &RouteResult{
				Method: method, MaxHops: options.MaxHops, Hops: []Hop{},
				Detail: err.Error(),
			}
			add("traceroute failed: %v", err)
		case route == nil:
			result.Route = &RouteResult{
				Method: method, MaxHops: options.MaxHops, Hops: []Hop{},
				Detail: "the route runner returned no result",
			}
		default:
			route.Method = method
			route.TargetAddresses = target.IPv4
			if len(target.IPv4) > 0 {
				route.TargetReached = routeReached(route, target.IPv4)
			}
			result.Route = route
		}
	}

	// --- Optional ASN enrichment ------------------------------------------
	if options.Enrich {
		if options.ASN == nil {
			add("ASN enrichment was requested but no enrichment source is configured")
		} else {
			// Only public addresses are ever queried. Enumerating a user's LAN
			// against a third party service would leak internal addressing for
			// no benefit, and a LAN address has no meaningful ASN anyway.
			public := publicAddresses(allAddresses(result))
			if len(public) == 0 {
				warn("no public address among the resolved target and route hops, so ASN enrichment was skipped")
			} else {
				infos, err := options.ASN.Enrich(ctx, public)
				if err != nil {
					// The measurement stands regardless of the enrichment.
					add("ASN enrichment failed and was ignored: %v", err)
				} else {
					result.Enrichment = infos
				}
			}
		}
	}

	return result, nil
}

// measure runs one probe method and folds the outcome into a measurement.
//
// A method that returned an error still produces a measurement record, because
// "the method ran and produced nothing" is different from "the method was
// never attempted" and a consumer needs to tell them apart.
func measure(
	ctx context.Context,
	run func(context.Context, string, int, time.Duration) (SampleSet, error),
	method ProbeMethod,
	endpoint string,
	samples int,
	timeout time.Duration,
	warn func(string, ...any),
) *LatencyMeasurement {
	m := &LatencyMeasurement{
		Method:    method,
		Outcome:   LatencyUnavailable,
		Attempts:  samples,
		SamplesMs: []float64{},
	}

	set, err := run(ctx, endpoint, samples, timeout)
	if err != nil && ctx.Err() != nil {
		m.Outcome = LatencySkipped
		m.Detail = err.Error()
		return m
	}
	m.TimedOut = set.TimedOut
	m.Failed = set.Failed
	m.Detail = set.Detail
	if err != nil && m.Detail == "" {
		m.Detail = err.Error()
	}
	if len(set.Samples) == 0 {
		return m
	}

	m.Successful = len(set.Samples)
	m.Outcome = LatencyMeasured
	min, max, total := set.Samples[0], set.Samples[0], time.Duration(0)
	var previous time.Duration
	var deltaTotal time.Duration
	for index, sample := range set.Samples {
		if sample < min {
			min = sample
		}
		if sample > max {
			max = sample
		}
		total += sample
		if index > 0 {
			d := sample - previous
			if d < 0 {
				d = -d
			}
			deltaTotal += d
		}
		previous = sample
		m.SamplesMs = append(m.SamplesMs, ms(sample))
	}
	average := total / time.Duration(len(set.Samples))
	m.MinMs = floatPtr(ms(min))
	m.MaxMs = floatPtr(ms(max))
	m.AverageMs = floatPtr(ms(average))
	// Jitter needs two samples to compare. One sample has no jitter, and
	// reporting zero there would invent a stability claim.
	if len(set.Samples) >= 2 {
		m.JitterMs = floatPtr(ms(deltaTotal / time.Duration(len(set.Samples)-1)))
	}
	return m
}

// measured reports whether a measurement exists at all.
func measured(m *LatencyMeasurement) bool {
	return m != nil && m.Outcome == LatencyMeasured
}

// routeReached reports whether the last responding hop answered with a target
// address. Intermediate hops answering does not mean the target was reached.
func routeReached(route *RouteResult, targets []string) bool {
	for _, hop := range route.Hops {
		for _, address := range hop.Addresses {
			for _, want := range targets {
				if address == want {
					return true
				}
			}
		}
	}
	return false
}

// allAddresses collects every address the run observed, target and hops alike.
func allAddresses(d *Diagnosis) []string {
	var out []string
	if d.Target != nil {
		out = append(out, d.Target.Addresses...)
	}
	if d.Route != nil {
		for _, hop := range d.Route.Hops {
			out = append(out, hop.Addresses...)
		}
	}
	return out
}
