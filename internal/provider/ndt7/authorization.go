package ndt7

// Single-use, per-measurement authorization.
//
// Two independent decisions guard this provider:
//
//  1. ConsentRecord - the durable, policy-versioned opt-in. It is stored
//     next to the GoSpeed configuration, it survives restarts, and it is
//     what makes Locate node discovery possible. A remembered agreement to
//     discovery is NOT remembered agreement to measuring.
//  2. RunGrant - the decision to run exactly ONE measurement, taken at the
//     moment the user is asked about that specific run. It is issued per
//     node and direction, consumed by the first matching Run, and never
//     remembered.
//
// The split exists because the two actions are different in kind. A Locate
// query publishes nothing; a measurement publishes a permanent dataset
// entry at M-Lab. Reusing the first decision for the second would mean that
// a single dialog, answered once, authorises an unbounded series of
// publications.
//
// The gate lives in Run, not in the GUI. Showing a confirmation dialog is a
// presentation decision; the enforcement point is the backend entry that
// opens the WebSocket, so a caller that never asked the user simply never
// receives an authorization and is refused before a single byte moves.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
)

var (
	// ErrRunAuthorizationRequired is returned by Run when no usable
	// per-measurement authorization exists: the user was never asked,
	// refused, dismissed the confirmation, or the grant was already
	// consumed by an earlier run.
	ErrRunAuthorizationRequired = errors.New("ndt7: this measurement was not authorized; a single-use authorization taken at the moment of the run is required, and the stored discovery consent does not authorize a measurement")
	// ErrRunAuthorizationMismatch is returned by Run when an
	// authorization exists but was issued for a different node or
	// direction than the run asks for.
	ErrRunAuthorizationMismatch = errors.New("ndt7: the measurement authorization does not cover this run")
)

// RunGrant is the evidence that the user authorized one specific
// measurement.
//
// It carries no credential: a grant is worthless without the runner that
// issued it, the runner honours it once, and the field set below is fixed.
// Printing one with %+v therefore cannot leak a service URL, an
// access_token or any other part of the endpoint.
type RunGrant struct {
	// Seq is a monotonic identifier, useful to correlate a confirmation
	// dialog with the run it authorised in logs and in tests.
	Seq int
	// NodeID and Direction are the measurement the grant is bound to. A
	// run against any other node or direction is refused.
	NodeID    string
	Direction provider.Direction
	// IssuedAt is when the decision was recorded.
	IssuedAt time.Time
}

// grantKey binds a pending authorization to one node and one direction.
type grantKey struct {
	nodeID    string
	direction provider.Direction
}

// AuthorizeRun records the user's explicit decision to run exactly one
// measurement against nodeID in direction.
//
// The caller must only call this after the user confirmed THIS run. It is
// deliberately a separate call from RecordConsent: recording consent says
// "this user accepts the M-Lab data policy", authorizing a run says "start
// this measurement now".
//
// An authorization is consumed by the first Run whose node and direction
// match. Issuing n authorizations permits n measurements and no more.
func (r *Runner) AuthorizeRun(nodeID string, direction provider.Direction) (*RunGrant, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil, errors.New("ndt7: a measurement authorization must name the node it authorizes")
	}
	switch direction {
	case provider.DirectionDownload, provider.DirectionUpload:
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedDirection, direction)
	}
	now := time.Now().UTC()

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingGrants == nil {
		r.pendingGrants = make(map[grantKey]int)
	}
	key := grantKey{nodeID: nodeID, direction: direction}
	r.grantSeq++
	r.pendingGrants[key]++
	return &RunGrant{Seq: r.grantSeq, NodeID: nodeID, Direction: direction, IssuedAt: now}, nil
}

// RevokeRunAuthorization drops every pending authorization.
//
// A user who refuses, dismisses or cancels the confirmation must leave no
// authorization behind. Calling it is not required for safety - an
// unauthorized run is refused anyway - but it makes the refusal observable
// and prevents a confirmation the user already dismissed from authorising a
// later run.
func (r *Runner) RevokeRunAuthorization() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pendingGrants = nil
}

// consumeRunAuthorization takes one authorization for exactly this node and
// direction.
//
// The grant is taken at the same gate as the daily counter and immediately
// before the connection opens, so the two are spent together: everything a
// pre-flight refusal rejects - a malformed request, a context that is
// already done, an exhausted daily limit, a missing or mismatched
// authorization - happens BEFORE this call and leaves the confirmation
// untouched. Once this call succeeds the confirmation is spent, whatever the
// run's outcome: a transfer that then fails or is cancelled did open a
// socket and did leave a server-side record, so a retry must be confirmed
// again.
//
// A caller that abandons a confirmation without running anything - the user
// dismisses the dialog, or cancels a run that never started - calls
// RevokeRunAuthorization. That is what stops a dismissal from authorizing a
// later run.
//
// The call is safe for concurrent use: two goroutines racing for a single
// pending authorization produce exactly one success.
func (r *Runner) consumeRunAuthorization(nodeID string, direction provider.Direction) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for key, count := range r.pendingGrants {
		total += count
		if key.nodeID == nodeID && key.direction == direction {
			if count <= 1 {
				delete(r.pendingGrants, key)
			} else {
				r.pendingGrants[key] = count - 1
			}
			return nil
		}
	}
	if total == 0 {
		return fmt.Errorf("%w: no pending authorization", ErrRunAuthorizationRequired)
	}
	// Something is pending, but not for this run. The message names nodes
	// and directions only; neither is a credential, and an endpoint never
	// reaches this string.
	return fmt.Errorf("%w: authorized for %s, asked for %q/%s",
		ErrRunAuthorizationMismatch, pendingSummary(r.pendingGrants), nodeID, direction)
}

// PendingRunAuthorizations reports how many authorizations are outstanding.
// A GUI shows it to make "this run is authorized, and only this run"
// observable. It returns a count, so it cannot carry a credential.
func (r *Runner) PendingRunAuthorizations() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, count := range r.pendingGrants {
		total += count
	}
	return total
}

// pendingSummary renders the pending bindings for an error message.
func pendingSummary(pending map[grantKey]int) string {
	if len(pending) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(pending))
	for key, count := range pending {
		entry := key.nodeID + "/" + string(key.direction)
		if count > 1 {
			entry = fmt.Sprintf("%s (x%d)", entry, count)
		}
		parts = append(parts, entry)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
