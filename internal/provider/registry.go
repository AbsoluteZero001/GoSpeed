package provider

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Registry errors. They are distinguishable so a caller can tell a wiring
// mistake apart from an unavailable provider.
var (
	// ErrProviderNotRegistered reports that no runner is registered for an ID.
	// It is returned instead of a default runner: silently substituting
	// another measurement service would change what the user asked for.
	ErrProviderNotRegistered = errors.New("provider: no runner registered for provider id")
	// ErrProviderAlreadyRegistered reports a duplicate registration. Replacing
	// a live runner in place would let one measurement silently change engine
	// halfway through, so a duplicate is always refused.
	ErrProviderAlreadyRegistered = errors.New("provider: a runner is already registered for provider id")
	// ErrNilRunner reports a registration attempt without a runner.
	ErrNilRunner = errors.New("provider: runner must not be nil")
	// ErrNilID reports a registration attempt with an empty provider ID.
	ErrNilID = errors.New("provider: id must not be empty")
)

// Closer releases the resources one provider instance owns. It is supplied at
// registration time together with the runner, so instance ownership is decided
// by the caller that built the runner and not by the registry.
type Closer func() error

// Registry holds the measurement providers an application may use and resolves
// them by ID.
//
// What a Registry deliberately is not:
//   - It is not a session manager. It holds no measurement session, no cancel
//     function and no run state; scheduling runs stays with the caller.
//   - It is not a discovery service and it never reaches the network.
//   - It is not a global singleton. Every consumer builds the registry it needs,
//     so tests and a future desktop and CLI binary stay independent.
//
// Registering never probes a node and never negotiates capabilities: the
// registry only records which runner is allowed to serve which ID. Negotiation
// stays an explicit, consented step of an actual run.
//
// A Registry is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	runners map[ID]Runner
	closers map[ID]Closer
	closed  bool
}

// NewRegistry returns an empty registry. It performs no I/O.
func NewRegistry() *Registry {
	return &Registry{
		runners: make(map[ID]Runner),
		closers: make(map[ID]Closer),
	}
}

// Register records runner as the implementation of id.
//
// A duplicate ID is refused with ErrProviderAlreadyRegistered and the existing
// runner is kept. closer may be nil when the runner owns nothing that needs
// releasing; it is called exactly once, by Close.
func (r *Registry) Register(id ID, runner Runner, closer Closer) error {
	if id == "" {
		return ErrNilID
	}
	if runner == nil {
		return fmt.Errorf("%w: %s", ErrNilRunner, id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("provider: registry is closed, cannot register %s", id)
	}
	if _, exists := r.runners[id]; exists {
		return fmt.Errorf("%w: %s", ErrProviderAlreadyRegistered, id)
	}
	r.runners[id] = runner
	if closer != nil {
		r.closers[id] = closer
	}
	return nil
}

// MustRegister is Register for package level wiring. It panics on a duplicate
// or nil registration because those are programming errors in a static wiring,
// never a runtime condition.
func (r *Registry) MustRegister(id ID, runner Runner, closer Closer) {
	if err := r.Register(id, runner, closer); err != nil {
		panic(err)
	}
}

// Lookup returns the runner registered for id. It returns
// ErrProviderNotRegistered when the provider is not available; it never
// substitutes another provider.
func (r *Registry) Lookup(id ID) (Runner, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	runner, ok := r.runners[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderNotRegistered, id)
	}
	return runner, nil
}

// Has reports whether id is registered. It performs no lookup side effect.
func (r *Registry) Has(id ID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.runners[id]
	return ok
}

// IDs returns the registered provider IDs in a stable order, so a UI list does
// not reshuffle between calls.
func (r *Registry) IDs() []ID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := sortedIDs(r.runners)
	return append([]ID(nil), ids...)
}

// Close releases every registered runner's resources exactly once. It is safe
// to call more than once and safe to call concurrently with Lookup.
//
// Closing does not cancel a running measurement: a run is owned by the caller
// that started it, and that caller keeps its context. Close only returns the
// pooled connections a provider instance created for itself.
func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	closers := make([]Closer, 0, len(r.closers))
	for _, id := range sortedIDs(r.closers) {
		closers = append(closers, r.closers[id])
	}
	r.runners = make(map[ID]Runner)
	r.closers = make(map[ID]Closer)

	var failures []error
	for _, close := range closers {
		if err := close(); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// sortedIDs keeps Close deterministic even though map order is not.
func sortedIDs[T any](values map[ID]T) []ID {
	ids := make([]ID, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
