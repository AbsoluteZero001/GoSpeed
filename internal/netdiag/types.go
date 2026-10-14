// Package netdiag implements link level network diagnostics: local network
// information, latency measurements and an IPv4 traceroute.
//
// What this package deliberately is not: a throughput measurement. It never
// moves a payload large enough to saturate a link and it never reports a rate.
// Link capacity stays the job of internal/speedtest.
//
// Three rules govern every value in this package:
//
//  1. A number is only published when it was actually observed. Anything that
//     could not be measured is the string Unknown, or a null field. Nothing is
//     defaulted to zero, and zero is never used to mean "not measured".
//  2. Measurement methods never substitute for one another. ICMP echo time,
//     TCP connect time and HTTP round trip time are reported as three separate
//     measurements; a failure of one is never presented as a property of
//     another. In particular an ICMP timeout is NOT packet loss: it only says
//     that one host did not answer an echo request.
//  3. A direction is never invented. The traceroute records the forward route
//     the probes took. It is not a round trip path and must not be described as
//     one.
package netdiag

import "time"

// SchemaVersion identifies the result layout. It changes whenever a field is
// removed or changes meaning, so a stored result can always be interpreted.
const SchemaVersion = "netdiag/1"

// Unknown is the literal every unmeasurable string value carries. It is never
// an empty string, because "" cannot be told apart from a value that was
// measured and happened to be empty.
const Unknown = "unknown"

// N/A is the literal used in the human readable summary for a measurement that
// does not apply to this target at all, as distinct from one that applies but
// failed.
const NotApplicable = "N/A"

// ProbeMethod names how a latency number was produced. The three methods are
// reported side by side and never averaged together.
type ProbeMethod string

const (
	// ProbeICMP is an ICMP echo request round trip to the host.
	ProbeICMP ProbeMethod = "icmp_echo_rtt"
	// ProbeTCP is the time to complete the TCP three way handshake.
	ProbeTCP ProbeMethod = "tcp_connect_rtt"
	// ProbeHTTP is the time from issuing an HTTP request to receiving the
	// first response byte.
	ProbeHTTP ProbeMethod = "http_rtt"
)

// LatencyOutcome states whether a probe produced a usable value.
type LatencyOutcome string

const (
	// LatencyMeasured means at least one sample succeeded.
	LatencyMeasured LatencyOutcome = "measured"
	// LatencyNotApplicable means the method does not apply to this target,
	// for example HTTP against a host that speaks no HTTP.
	LatencyNotApplicable LatencyOutcome = "not_applicable"
	// LatencyUnavailable means the method applies but nothing answered.
	LatencyUnavailable LatencyOutcome = "unavailable"
	// LatencySkipped means the method was deliberately not run.
	LatencySkipped LatencyOutcome = "skipped"
)

// LatencyMeasurement is one latency measurement method applied to the target.
//
// Every numeric field is a pointer. A nil pointer means "not measured" and is
// the only correct representation of a missing value: a measured 0 ms is a
// real observation and stays a non-nil pointer to 0.
type LatencyMeasurement struct {
	Method ProbeMethod `json:"method"`
	// Outcome says whether the numbers below exist at all. A consumer must
	// read this before it reads a value, because a non-measured method leaves
	// every numeric field nil rather than zero.
	Outcome LatencyOutcome `json:"outcome"`
	// Attempts is how many probes were issued, including the ones that failed.
	Attempts int `json:"attempts"`
	// Successful is how many probes produced a value.
	Successful int `json:"successful"`
	// TimedOut is how many probes hit the per probe timeout.
	TimedOut int `json:"timed_out"`
	// Failed is how many probes failed for another reason, such as an
	// unreachable network or a refused connection.
	Failed int `json:"failed"`
	// MinMs, AverageMs, MaxMs and JitterMs are in milliseconds with the
	// fractional part preserved. They are nil unless Outcome is
	// LatencyMeasured.
	MinMs     *float64 `json:"min_ms"`
	AverageMs *float64 `json:"average_ms"`
	MaxMs     *float64 `json:"max_ms"`
	// JitterMs is the mean absolute difference between consecutive samples,
	// the same definition internal/speedtest uses for its own Jitter. It is
	// nil when fewer than two samples succeeded, because there is nothing to
	// compare.
	JitterMs *float64 `json:"jitter_ms"`
	// SamplesMs is every successful sample in milliseconds, in issue order.
	SamplesMs []float64 `json:"samples_ms"`
	// Notes carries method specific caveats, for example a note that ICMP
	// timeouts are not packet loss.
	Notes []string `json:"notes,omitempty"`
	// Detail explains a non-measured outcome. It is empty when measured.
	Detail string `json:"detail,omitempty"`
}

// LatencyResult holds the three measurement methods separately. They are never
// merged: an average across methods would be a number no probe ever observed.
type LatencyResult struct {
	ICMP *LatencyMeasurement `json:"icmp"`
	TCP  *LatencyMeasurement `json:"tcp"`
	HTTP *LatencyMeasurement `json:"http"`
}

// InterfaceInfo is one network interface as the operating system reports it.
//
// The fields are what the OS reports and nothing more. In particular Name and
// HardwareAddr are not used to infer a speed: an interface's nominal link speed
// is not a measured throughput and this package never reports one.
type InterfaceInfo struct {
	Name         string   `json:"name"`
	Index        int      `json:"index"`
	MTU          int      `json:"mtu"`
	HardwareAddr string   `json:"hardware_addr,omitempty"`
	Up           bool     `json:"up"`
	Addresses    []string `json:"addresses"`
	IPv4         []string `json:"ipv4"`
	IPv6         []string `json:"ipv6"`
}

// NetworkInfo describes the local side of the path.
//
// DefaultInterface and RouteToTarget are different questions and must not be
// merged. The default interface is the OS default route's interface, which is
// what a packet would use with no destination. RouteToTarget is the interface
// and source address the OS actually selects for the diagnosed target. On a
// machine with a VPN, several uplinks or policy routing these differ, and a
// traceroute result is only interpretable next to the second one.
type NetworkInfo struct {
	Interfaces []InterfaceInfo `json:"interfaces"`
	// DefaultInterface is the interface name of the OS default route, or
	// Unknown when it could not be determined.
	DefaultInterface string `json:"default_interface"`
	// DefaultGateway is the IPv4 gateway of the default route, or Unknown.
	DefaultGateway string `json:"default_gateway"`
	// DNSServers are the resolvers the OS is configured with, in the order the
	// OS reported them.
	DNSServers []string `json:"dns_servers"`
	// RouteToTarget is what the OS would actually use to reach the target.
	RouteToTarget *RouteSelection `json:"route_to_target"`
	// Scope classifies the *target* of the diagnosis using the same vocabulary
	// internal/speedtest applies to a measured target: local, lan, remote or
	// unknown.
	//
	// It is deliberately not derived from the local source address. A machine
	// on 192.168.1.111 reaching 1.1.1.1 is measuring a remote path, and calling
	// that "lan" would describe the user's own LAN instead of the link being
	// measured. The local side has its own field.
	Scope string `json:"scope"`
	// LocalScope classifies the source address the operating system selected.
	// A diagnosis against a private target on a private source is the only case
	// where both are lan; a private source reaching a public target is remote.
	//
	// This field was added after a release candidate; see the schema note in the
	// documentation. It is additive and existing consumers that ignore it are
	// unaffected.
	LocalScope string `json:"local_scope"`
	// Notes carries platform caveats, for example that the gateway could not be
	// read on this operating system.
	Notes []string `json:"notes,omitempty"`
}

// RouteSelection is the OS routing decision for one destination. It is derived
// by asking the OS which local address it would bind for that destination,
// which does not transmit a packet.
type RouteSelection struct {
	// Destination is the resolved address the decision applies to.
	Destination string `json:"destination"`
	// LocalAddress is the source address the OS would send from.
	LocalAddress string `json:"local_address"`
	// Interface is the interface the OS would use, or Unknown when the address
	// alone does not identify one unambiguously.
	Interface string `json:"interface"`
	// Method names how this was determined, so a reader knows it is a routing
	// table lookup rather than an observation of a packet.
	Method string `json:"method"`
}

// HopState is the observed state of one traceroute hop.
type HopState string

const (
	// HopResponding means at least one probe to this hop was answered.
	HopResponding HopState = "responding"
	// HopTimeout means no probe to this hop was answered within the wait.
	// A timeout is not evidence that the hop is down: routers commonly rate
	// limit or deprioritise TTL expired messages.
	HopTimeout HopState = "timeout"
	// HopUnparsed means a probe returned something this parser did not
	// recognise. The hop is kept rather than dropped so the hop count stays
	// honest.
	HopUnparsed HopState = "unparsed"
)

// Hop is one TTL step of the forward route.
type Hop struct {
	Index int `json:"index"`
	// TTL is the hop's distance in router hops as configured, not the number
	// of hops that answered.
	TTL int `json:"ttl"`
	// Address is the responding address, or Unknown for a timeout. A hop may
	// answer from several addresses; the first answering one is recorded here
	// and every sample is listed.
	Address string `json:"address"`
	// Addresses lists every responding address seen for this hop.
	Addresses []string `json:"addresses"`
	State     HopState `json:"state"`
	// RttMs is the best round trip of this hop's probes in milliseconds, or
	// nil when nothing answered. It is a single forward probe round trip and
	// says nothing about the return path.
	RttMs *float64 `json:"rtt_ms"`
	// RttMsAll is every probe round trip for this hop, in probe order.
	RttMsAll []float64 `json:"rtt_ms_all"`
	// Probes is how many probes were issued to this hop.
	Probes int `json:"probes"`
	// Responded is how many of them answered.
	Responded int `json:"responded"`
	// Hostnames holds reverse DNS names when the platform traceroute
	// resolved them. It is empty when resolution was not requested.
	Hostnames []string `json:"hostnames,omitempty"`
	// Notes carries hop specific caveats, for example that the platform
	// published an upper bound instead of a round trip.
	Notes []string `json:"notes,omitempty"`
}

// RouteResult is the forward route the probes took.
//
// It is a forward path only. The return path is not measured and is not
// inferable from this result.
type RouteResult struct {
	Method string `json:"method"`
	// MaxHops is the TTL ceiling the probe was run with.
	MaxHops int `json:"max_hops"`
	// Hops is ordered by TTL and always starts at index 1. A gap is never
	// compressed away.
	Hops []Hop `json:"hops"`
	// TargetReached is true when the final hop answered with an address that
	// matches a resolved target address.
	TargetReached bool `json:"target_reached"`
	// TargetAddresses are the addresses the route was probed to.
	TargetAddresses []string `json:"target_addresses"`
	// Notes carries parsing caveats.
	Notes []string `json:"notes,omitempty"`
	// Detail explains why the route is absent or partial.
	Detail string `json:"detail,omitempty"`
}

// TargetInfo is the resolved target of the diagnosis.
type TargetInfo struct {
	Input    string `json:"input"`
	Hostname string `json:"hostname"`
	// Addresses are every resolved address, IPv4 first.
	Addresses []string `json:"addresses"`
	IPv4      []string `json:"ipv4"`
	// Port is the TCP port probed for the TCP connect measurement.
	Port int `json:"port"`
	// Resolver records which resolver produced the addresses.
	Resolver string `json:"resolver"`
	// Resolved is false when name resolution failed. Addresses is then empty
	// and every dependent measurement reports why.
	Resolved bool   `json:"resolved"`
	Detail   string `json:"detail,omitempty"`
}

// ASInfo is one autonomous system attribution for an address.
//
// It describes who registers an address, not which path a packet took. A
// traceroute hop address and an ASN together say who owns that address, which
// is not the same as who forwarded the packet.
type ASInfo struct {
	// Address is the queried address.
	Address string `json:"address"`
	// ASN is the autonomous system number, 0 when unknown.
	ASN uint32 `json:"asn"`
	// Holder is the registered organisation name, or Unknown.
	Holder string `json:"holder"`
	// Prefix is the registered prefix the address falls in.
	Prefix string `json:"prefix,omitempty"`
	// Source names the data provider so a reader knows where it came from.
	Source string `json:"source,omitempty"`
	// Detail explains an absent or failed lookup. An enrichment failure never
	// removes or alters the underlying measurement.
	Detail string `json:"detail,omitempty"`
}

// Diagnosis is the complete result of one diagnose run.
type Diagnosis struct {
	SchemaVersion string         `json:"schema_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Network       *NetworkInfo   `json:"network"`
	Target        *TargetInfo    `json:"target"`
	Latency       *LatencyResult `json:"latency"`
	Route         *RouteResult   `json:"route"`
	// Enrichment holds optional third party attributions. It is nil when
	// enrichment was not requested, and an entry with an empty Detail field
	// means "not queried" while a filled Detail means "queried and failed".
	Enrichment []*ASInfo `json:"enrichment,omitempty"`
	// Warnings are non fatal observations a reader must not miss.
	//
	// Both slices are always present in the encoding, empty rather than
	// omitted, so a consumer can read them without first testing whether the
	// key exists. A schema whose keys come and go with the data is a schema
	// that breaks consumers on an empty run.
	Warnings []string `json:"warnings"`
	// Errors are the failures that reduced coverage of this result.
	Errors []string `json:"errors"`
}

// ms converts a duration to fractional milliseconds.
func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// floatPtr returns a pointer to v. It keeps the "nil means unmeasured"
// convention readable at every call site instead of writing &v inline.
func floatPtr(v float64) *float64 { return &v }
