package main

// The M-Lab NDT7 integration for the desktop client.
//
// Three independent gates stand between the user and a public measurement:
//
//  1. Consent - the durable, versioned opt-in to the M-Lab data policy. It
//     gates node discovery and every measurement.
//  2. Run authorization - the single use decision to run exactly one
//     measurement against one node in one direction.
//  3. The daily limit - at most four measurements per calendar day.
//
// The desktop client never weakens them. The frontend receives no access
// token, no service URL and no way to express "already authorized": it names
// a node by an opaque reference and the backend resolves that reference to
// the real endpoint. The provider consumes the authorization and enforces
// the daily limit inside Run, so a caller that skipped the GUI is refused
// there, before a single byte moves.
//
// The GUI is a presentation layer, not a gate. Everything the GUI does
// (showing the notice, asking for the per-run confirmation) is worth doing
// because the user is the one who has to live with the consequences, but the
// enforcement lives in the backend so that a frontend that lies, a script in
// the webview or a stale build cannot get around it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
)

// ndt7ProviderID is the provider this file integrates.
const ndt7ProviderID = provider.IDMLabNDT7

// ndt7ConsentFileVersion is the on-disk schema version of the stored
// consent decision.
//
// A file written by another version is refused rather than guessed at: a
// silently reset consent record would let a measurement run under a wording
// the user never read.
const ndt7ConsentFileVersion = 1

// Errors the desktop layer raises for the NDT7 path. They are sentinels so a
// caller can tell them apart, and their texts never contain an endpoint, a
// token or any other part of a credential.
var (
	// errNDT7ConsentRequired is returned when a consent bound action is
	// attempted before the user accepted the M-Lab disclosure.
	errNDT7ConsentRequired = errors.New("desktop: M-Lab 测速需要先阅读并同意隐私告知（ndt7-notice）")
	// errNDT7UnknownDirection is returned for a direction the protocol does
	// not carry.
	errNDT7UnknownDirection = errors.New("desktop: M-Lab 测速方向必须是 download 或 upload")
)

// ndt7Gateway owns the NDT7 provider instance and the desktop side of its
// three gates.
type ndt7Gateway struct {
	mu      sync.Mutex
	runner  *ndt7.Runner
	nodes   *providerNodeStore
	daily   ndt7.DailyRunStore
	consent consentStore
	// agreed records whether a consent whose wording version is the current
	// one is on record. It mirrors the provider's own gate and exists so the
	// GUI can say "please confirm again" instead of "unknown".
	agreed bool
	// confirmMu serialises the issuing of run authorizations, so two
	// confirmations that arrive at the same time cannot become two grants.
	//
	// The authorizations themselves live in the provider, which is the only
	// holder of them. The desktop keeps no second book of them: two books
	// have to be kept in step, and a client that fell behind would either
	// refuse a measurement the user had confirmed or let one confirmation
	// buy two measurements.
	confirmMu sync.Mutex
}

// ndt7GatewayOptions configures a gateway. The zero value asks for the
// production wiring; the fields exist so a test can supply an in-memory
// counter and an in-memory consent store.
type ndt7GatewayOptions struct {
	runner  *ndt7.Runner
	nodes   *providerNodeStore
	daily   ndt7.DailyRunStore
	consent consentStore
}

// newNDT7Gateway builds the gateway against the user's real configuration
// directory: the persisted consent decision and the persisted daily counter
// live next to the rest of the GoSpeed configuration, so removing that
// directory removes both.
//
// Nothing is measured or probed here. The counter file is not read until the
// first count, and the consent file is only read, never written.
func newNDT7Gateway() (*ndt7Gateway, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("desktop: cannot locate the user configuration directory: %w", err)
	}
	dailyPath, err := ndt7.DefaultDailyRunStorePath()
	if err != nil {
		return nil, err
	}
	return newNDT7GatewayWith(ndt7GatewayOptions{
		daily:   ndt7.NewFileDailyRunStore(dailyPath),
		consent: &fileConsentStore{path: filepath.Join(dir, "GoSpeed", "ndt7-consent.json")},
	})
}

// newNDT7GatewayWith builds a gateway from explicit parts.
//
// The stored consent is applied only when it is current: an agreement given
// under an older wording does not carry over, because the user has to read
// the new wording before it applies. The provider refuses a stale record on
// its own as well; this check exists so the GUI can explain what happened
// instead of failing later with a generic refusal.
func newNDT7GatewayWith(options ndt7GatewayOptions) (*ndt7Gateway, error) {
	gateway := &ndt7Gateway{
		nodes:   options.nodes,
		daily:   options.daily,
		consent: options.consent,
	}
	if gateway.nodes == nil {
		gateway.nodes = newProviderNodeStore()
	}
	gateway.runner = options.runner
	if gateway.runner == nil {
		gateway.runner = ndt7.NewRunner()
	}
	if gateway.daily != nil {
		gateway.runner.SetDailyRunStore(gateway.daily)
	}
	if gateway.consent != nil {
		record, err := gateway.consent.load()
		switch {
		case err != nil:
			// A consent document that cannot be read is not a consent. The
			// user is asked again. The unreadable file is left untouched: a
			// damaged file must never be silently replaced by an empty one.
		case record == nil:
			// No decision was stored yet.
		case record.Agreed && record.PolicyVersion == ndt7.ConsentVersion():
			if err := gateway.runner.RecordConsent(true, record.AgreedAt, record.PolicyVersion); err != nil {
				return nil, err
			}
			gateway.agreed = true
		default:
			// The wording changed, or the user refused. The stored decision
			// does not apply any more.
		}
	}
	return gateway, nil
}

// runner returns the provider instance the gateway owns.
func (g *ndt7Gateway) provider() *ndt7.Runner { return g.runner }

// consentAgreed reports whether a current consent is on record.
func (g *ndt7Gateway) consentAgreed() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.agreed
}

// state returns the GUI facing snapshot of the three gates.
//
// It carries the full disclosure text (the GUI shows it verbatim), the state
// of today's counter and the provider's traffic budget. It never carries a
// credential, an endpoint or a node address.
func (g *ndt7Gateway) state() NDT7ConsentState {
	state := NDT7ConsentState{
		Provider:        string(ndt7ProviderID),
		ConsentRequired: !g.consentAgreed(),
		Agreed:          g.consentAgreed(),
		PolicyVersion:   ndt7.ConsentVersion(),
		Notice:          g.runner.ConsentNotice(),
		DailyLimit:      ndt7.MaxDailyRuns,
		NodeCount:       len(g.nodes.allViews()),
	}
	budget := g.runner.Plan()
	state.Budget = NDT7BudgetView{
		DownloadBytes: budget.DownloadBytes,
		UploadBytes:   budget.UploadBytes,
		TotalBytes:    budget.TotalBytes,
	}
	if g.daily != nil {
		used, err := g.daily.Used(time.Now())
		if err != nil {
			// The counter is unreadable, so the limit cannot be enforced.
			// The provider fails closed on the same condition; the GUI shows
			// the reason instead of a number.
			state.DailyAvailable = false
			state.DailyError = err.Error()
		} else {
			state.DailyAvailable = true
			state.DailyUsed = used
			state.DailyRemaining = ndt7.MaxDailyRuns - used
			if state.DailyRemaining < 0 {
				state.DailyRemaining = 0
			}
		}
	}
	return state
}

// recordConsent stores the user's decision about the M-Lab disclosure.
//
// Agreeing enables node discovery and measurements. Refusing withdraws
// everything the agreement had enabled: the pending run authorizations are
// dropped and the discovered nodes, and with them the short lived access
// token the Locate answer carried, are discarded.
func (g *ndt7Gateway) recordConsent(agreed bool) (NDT7ConsentState, error) {
	now := time.Now()
	// The decision is stored before the provider is told about it. A write
	// that fails therefore leaves the consent in force nowhere: the stored
	// record and the provider agree, and the panel shows what really
	// happened. The opposite order would let the provider act on a consent
	// that was never persisted, which the panel would then report as
	// missing.
	if g.consent != nil {
		record := ndt7.ConsentRecord{
			Agreed:        agreed,
			AgreedAt:      now,
			PolicyVersion: ndt7.ConsentVersion(),
			ClientName:    ndt7.ClientName,
			ClientVersion: ndt7.ClientVersion,
		}
		if err := g.consent.save(record); err != nil {
			return NDT7ConsentState{}, err
		}
	}
	if err := g.runner.RecordConsent(agreed, now, ndt7.ConsentVersion()); err != nil {
		return NDT7ConsentState{}, err
	}
	g.mu.Lock()
	g.agreed = agreed
	g.mu.Unlock()
	if !agreed {
		g.revokeAuthorizations()
		g.nodes.forget()
	}
	return g.state(), nil
}

// discover asks the M-Lab Locate service for the NDT7 servers near the
// client and publishes them into the node store.
//
// The query itself reveals the client public IP to M-Lab, so it is a consent
// bound action: without a current consent the request is never sent. The
// published nodes are handed to the GUI as views, so no access token leaves
// the backend.
func (g *ndt7Gateway) discover(ctx context.Context) ([]ProviderNodeView, error) {
	if !g.consentAgreed() {
		return nil, errNDT7ConsentRequired
	}
	nodes, err := g.runner.Discover(ctx)
	if err != nil {
		return nil, describeDiscoveryError(err)
	}
	return g.nodes.publish(nodes), nil
}

// describeDiscoveryError turns a provider refusal into a message the user
// can act on.
//
// The provider's own errors are precise and stable, and they stay matchable
// through errors.Is because the original error is wrapped. Only the wording
// around them is added: the GUI is Chinese, and a bare English sentinel
// tells the user nothing about what to do next.
func describeDiscoveryError(err error) error {
	switch {
	case errors.Is(err, ndt7.ErrConsentRequired):
		return fmt.Errorf("尚未同意 M-Lab 隐私告知，无法查询节点：%w", err)
	case errors.Is(err, ndt7.ErrLocateRateLimited):
		return fmt.Errorf("M-Lab 要求推迟下一次节点查询，请稍后再试：%w", err)
	case errors.Is(err, ndt7.ErrLocateRedirect):
		return fmt.Errorf("M-Lab 的节点查询返回了重定向，出于安全考虑不予跟随：%w", err)
	case errors.Is(err, ndt7.ErrNoLocateServers):
		return fmt.Errorf("M-Lab 没有返回可用的测速节点：%w", err)
	case errors.Is(err, ndt7.ErrNoValidLocateServer):
		return fmt.Errorf("M-Lab 返回的节点都没有通过安全校验：%w", err)
	default:
		return fmt.Errorf("无法发现 M-Lab 节点：%w", err)
	}
}

// authorizeRun records the decision to run exactly one measurement against
// one node in one direction.
//
// The caller must only call this after the user confirmed THIS run: the GUI
// shows the per-run disclosure first. The grant is bound to the node and the
// direction, it is consumed by the first matching Run, and it is never
// remembered.
//
// The node is named by an opaque reference. The endpoint is resolved from the
// store inside this package, so a caller cannot point the measurement at an
// address of its own choosing.
func (g *ndt7Gateway) authorizeRun(ref string, direction provider.Direction) (NDT7RunAuthorization, error) {
	node, ok := g.nodes.nodeFor(ref)
	if !ok {
		return NDT7RunAuthorization{}, fmt.Errorf("%w: %q", errUnknownNodeRef, ref)
	}
	// The issuing is serialised, so two confirmations that arrive at the
	// same time cannot become two grants.
	g.confirmMu.Lock()
	defer g.confirmMu.Unlock()
	if g.runner.HasRunAuthorization(node.ID, direction) {
		// A confirmation for exactly this measurement is already
		// outstanding. It is reused rather than replaced: a double click is
		// one confirmation, and one confirmation buys one measurement.
		return NDT7RunAuthorization{
			Granted:   true,
			NodeID:    node.ID,
			Direction: string(direction),
		}, nil
	}
	grant, err := g.runner.AuthorizeRun(node.ID, direction)
	if err != nil {
		return NDT7RunAuthorization{}, err
	}
	return NDT7RunAuthorization{
		Granted:   true,
		NodeID:    node.ID,
		Direction: string(direction),
		Seq:       grant.Seq,
	}, nil
}

// revokeAuthorizations drops every pending run authorization.
//
// It is called when the user refuses, when a session ends and when the
// application shuts down. A confirmation the user already dismissed must not
// authorize a later run.
func (g *ndt7Gateway) revokeAuthorizations() {
	g.confirmMu.Lock()
	defer g.confirmMu.Unlock()
	g.runner.RevokeRunAuthorization()
}

// endSession ends an NDT7 session: the pending authorizations are dropped
// and the discovered nodes, and with them the short lived access token the
// Locate answer carried, are discarded.
//
// The daily counter is deliberately not touched. It lives in a file, so it
// survives a restart, a window reload and a crash.
func (g *ndt7Gateway) endSession() {
	g.revokeAuthorizations()
	g.nodes.forget()
}

// parseNDT7Direction maps the GUI's direction onto the protocol's.
//
// The protocol carries exactly two directions and measures one of them per
// run. Anything else is a caller error, never a silent default: a request
// for "both" must not be answered with a download.
func parseNDT7Direction(value string) (provider.Direction, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(provider.DirectionDownload):
		return provider.DirectionDownload, nil
	case string(provider.DirectionUpload):
		return provider.DirectionUpload, nil
	default:
		return "", fmt.Errorf("%w: %q", errNDT7UnknownDirection, value)
	}
}

// consentStore persists the user's consent decision.
//
// The stored document holds the decision, the time and the wording version
// it was given under. It holds no credential, no endpoint and no node list.
type consentStore interface {
	// load returns the stored decision, or nil when there is none.
	load() (*ndt7.ConsentRecord, error)
	// save writes the decision.
	save(ndt7.ConsentRecord) error
}

// fileConsentStore stores the decision in the user's configuration
// directory.
type fileConsentStore struct {
	path string
}

// consentFile is the on-disk document. It records the decision and the
// wording version only.
type consentFile struct {
	Version       int       `json:"version"`
	Agreed        bool      `json:"agreed"`
	AgreedAt      time.Time `json:"agreedAt"`
	PolicyVersion string    `json:"policyVersion"`
	ClientName    string    `json:"clientName"`
	ClientVersion string    `json:"clientVersion"`
}

// load implements consentStore.
func (s *fileConsentStore) load() (*ndt7.ConsentRecord, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("desktop: the stored NDT7 consent at %s cannot be read: %w", s.path, err)
	}
	var file consentFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("desktop: the stored NDT7 consent at %s is not readable as a GoSpeed consent record", s.path)
	}
	if file.Version != ndt7ConsentFileVersion {
		return nil, fmt.Errorf("desktop: the stored NDT7 consent at %s has version %d, this build writes version %d",
			s.path, file.Version, ndt7ConsentFileVersion)
	}
	return &ndt7.ConsentRecord{
		Agreed:        file.Agreed,
		AgreedAt:      file.AgreedAt,
		PolicyVersion: file.PolicyVersion,
		ClientName:    file.ClientName,
		ClientVersion: file.ClientVersion,
	}, nil
}

// save implements consentStore.
//
// The write goes through a temporary file and a rename, so a reader never
// observes a half written document and an interrupted write cannot turn the
// stored consent into "no consent".
func (s *fileConsentStore) save(record ndt7.ConsentRecord) error {
	document := consentFile{
		Version:       ndt7ConsentFileVersion,
		Agreed:        record.Agreed,
		AgreedAt:      record.AgreedAt,
		PolicyVersion: record.PolicyVersion,
		ClientName:    record.ClientName,
		ClientVersion: record.ClientVersion,
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("desktop: the NDT7 consent record cannot be encoded: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("desktop: the NDT7 consent directory cannot be created: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("desktop: the NDT7 consent file cannot be created: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("desktop: the NDT7 consent file cannot be restricted: %w", err)
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return fmt.Errorf("desktop: the NDT7 consent file cannot be written: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("desktop: the NDT7 consent file cannot be closed: %w", err)
	}
	if err := os.Rename(tempName, s.path); err != nil {
		return fmt.Errorf("desktop: the NDT7 consent file cannot be replaced: %w", err)
	}
	return nil
}

// memoryConsentStore keeps the decision in memory. It is what the tests
// use so that no test ever writes into the user's configuration directory.
type memoryConsentStore struct {
	mu     sync.Mutex
	record *ndt7.ConsentRecord
}

// load implements consentStore.
func (s *memoryConsentStore) load() (*ndt7.ConsentRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.record == nil {
		return nil, nil
	}
	copied := *s.record
	return &copied, nil
}

// save implements consentStore.
func (s *memoryConsentStore) save(record ndt7.ConsentRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	copied := record
	s.record = &copied
	return nil
}

// runNDT7Test executes one M-Lab NDT7 measurement.
//
// The endpoint is resolved here, in the backend, from the opaque reference
// the GUI sent. The GUI never holds a service URL, so it cannot point the
// measurement at an address of its own choosing.
//
// The run itself is handed to the provider, which owns the three gates: it
// refuses a request without a current consent, without a single use
// authorization for this node and direction, or when today's counter is
// exhausted - each of them before a single byte moves.
func (r *Runner) runNDT7Test(ctx context.Context, session *session, options StartOptions, finished *FinishedEvent) {
	gateway, err := r.ndt7Gateway()
	if err != nil {
		setFinishedError(finished, err)
		return
	}
	// The session ends with the run: the pending authorization is dropped and
	// the short lived access token the Locate answer carried is discarded.
	defer gateway.endSession()

	// The provider has to be registered in this build before a run may use
	// it. The registry is the statement of what this build may measure with,
	// so a run that cannot find its provider is refused rather than silently
	// measured by something else.
	registry, err := r.registry()
	if err != nil {
		setFinishedError(finished, err)
		return
	}
	if _, err := registry.Lookup(ndt7ProviderID); err != nil {
		setFinishedError(finished, fmt.Errorf("%w: %s: %w", errProviderUnavailable, ndt7ProviderID, err))
		return
	}

	direction, err := parseNDT7Direction(options.Direction)
	if err != nil {
		setFinishedError(finished, err)
		return
	}
	node, ok := gateway.nodes.nodeFor(options.ProviderNodeRef)
	if !ok {
		setFinishedError(finished, fmt.Errorf("%w: %q", errUnknownNodeRef, options.ProviderNodeRef))
		return
	}
	// The endpoint comes from the store, never from the request: the request
	// has no field for one.
	endpoint, err := gateway.nodes.endpointFor(options.ProviderNodeRef)
	if err != nil {
		setFinishedError(finished, err)
		return
	}
	resolved := node
	resolved.Endpoint = endpoint

	// Negotiation runs behind the consent gate inside the provider, so a
	// withdrawn consent is refused here rather than in the middle of a
	// measurement.
	capabilities, err := gateway.provider().Negotiate(ctx, resolved)
	if err != nil {
		if !errors.Is(err, ndt7.ErrConsentRequired) {
			setFinishedError(finished, err)
			return
		}
		// The consent is missing. The provider's own run gate produces the
		// authoritative verdict for that, so the request is still handed
		// over and the refusal reaches the GUI as a unified result rather
		// than as a bare error string.
		capabilities = provider.CapabilitySet{}
	}

	view, _ := gateway.nodes.view(options.ProviderNodeRef)
	notice := noticeForProviderNode(view)
	r.emit(EventTarget, TargetEvent{
		Target: NodeTargetView{
			ID:           resolved.ID,
			Name:         resolved.Name,
			BaseURL:      providerNodeDisplayHost(view),
			Protocol:     string(ndt7ProviderID),
			Local:        false,
			NetworkScope: string(resolved.Scope),
		},
		SelectionMethod: "provider-discovery",
		SelectionReason: "M-Lab Locate 发现的公共测速节点",
		Notice:          &notice,
	})

	request := provider.RunRequest{
		Node:         resolved,
		Capabilities: capabilities,
		Directions:   []provider.Direction{direction},
		// NDT7 measures one direction over a single WebSocket. The provider
		// refuses anything else, so the request states the protocol's shape
		// instead of letting it fail later.
		Connections: 1,
		// The provider applies its own traffic budget and timeout. The
		// engine's duration and sample cadence do not exist in this protocol.
		Budget: gateway.provider().Plan(),
		Events: &progressSink{runner: r, session: session},
	}
	final, runErr := gateway.provider().Run(ctx, request)
	applyFinished(finished, final, runErr)
}
