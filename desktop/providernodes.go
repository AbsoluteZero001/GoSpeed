package main

// The GUI serialization boundary for provider-discovered nodes.
//
// provider.Node.Endpoint holds the COMPLETE service URL a Locate answer
// returned, and that URL carries a short-lived access_token. Marshalling a
// provider.Node therefore publishes a usable credential: into the frontend
// bundle, into any developer console dump, into a crash report that
// captures the payload, and into any state a UI library persists.
//
// The boundary is a projection, not a filter. Filtering a JSON blob by
// deleting keys that look wrong fails the day a new field is added, so the
// GUI receives a type that cannot hold a credential in the first place:
//
//   - the runtime provider.Node stays in the backend, keyed by an opaque
//     reference the backend minted;
//   - ProviderNodeView is the only node shape the frontend ever sees;
//   - the frontend names a node by Ref in a later run request, and the
//     backend resolves Ref to the full endpoint through endpointFor.
//
// The frontend therefore never receives a token and never sends one back.
// A caller that wants the endpoint must go through the backend, which is
// the only place the discovery cache lives.

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
)

// ProviderNodeView is the GUI-safe projection of a provider.Node.
//
// Every field is either display metadata or a classification enum. The type
// deliberately has no Endpoint, no URL and no field capable of holding a
// query string; providernodes_test.go pins the field set by reflection so
// adding one back is a deliberate, reviewed act rather than an accident.
type ProviderNodeView struct {
	// Ref is the opaque handle the GUI passes back to start a run. It is
	// minted by the backend from a counter, so it carries no address, no
	// node name and no credential, and a value the frontend invented will
	// not resolve.
	Ref string `json:"ref"`
	// ID is the provider's node identifier. It is a hostname and is safe
	// to display; it is not sufficient to start a run on its own.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Host is the endpoint's host and port with the scheme and everything
	// after the authority removed. It is derived by parsing, never by
	// truncating a string, so a malformed endpoint cannot smuggle a token
	// through.
	Host string `json:"host"`
	// ServicePath is the endpoint's path, which for NDT7 is
	// /ndt/v7/download or /ndt/v7/upload. The query string that carries
	// the credential is dropped.
	ServicePath string `json:"servicePath"`
	Scope       string `json:"scope"`
	Provider    string `json:"provider"`
	Ownership   string `json:"ownership"`
	Discovery   string `json:"discovery"`
}

// errUnknownNodeRef is returned when a caller asks for a node the backend
// never published. The reference is safe to name in the message: the
// backend minted it and it contains no address.
var errUnknownNodeRef = errors.New("desktop: unknown provider node reference")

// providerNodeStore keeps the runtime nodes in the backend and hands out
// only views.
//
// It replaces its whole contents on every publish: a Locate answer carries
// a short-lived token, so a refreshed answer supersedes the previous one
// completely rather than merging into it, and a reference minted for a
// superseded token stops resolving.
type providerNodeStore struct {
	mu    sync.RWMutex
	seq   int
	nodes map[string]provider.Node
	views map[string]ProviderNodeView
	byID  map[string]string
}

func newProviderNodeStore() *providerNodeStore {
	return &providerNodeStore{
		nodes: make(map[string]provider.Node),
		views: make(map[string]ProviderNodeView),
		byID:  make(map[string]string),
	}
}

// publish registers a discovery answer and returns the views the GUI may
// receive.
//
// It returns one view per accepted node, in the order the provider listed
// them. The full provider.Node values stay inside the store.
func (s *providerNodeStore) publish(nodes []provider.Node) []ProviderNodeView {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = make(map[string]provider.Node, len(nodes))
	s.views = make(map[string]ProviderNodeView, len(nodes))
	s.byID = make(map[string]string, len(nodes))
	out := make([]ProviderNodeView, 0, len(nodes))
	for _, node := range nodes {
		s.seq++
		ref := fmt.Sprintf("pnode-%d", s.seq)
		view := newProviderNodeView(ref, node)
		s.nodes[ref] = node
		s.views[ref] = view
		s.byID[node.ID] = ref
		out = append(out, view)
	}
	return out
}

// view returns the GUI-safe projection of one published node.
func (s *providerNodeStore) view(ref string) (ProviderNodeView, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	view, ok := s.views[ref]
	return view, ok
}

// allViews returns every published projection.
//
// The order is by reference, and a reference is minted from a counter, so
// two identical calls return the same sequence and a UI list does not
// reshuffle between refreshes.
func (s *providerNodeStore) allViews() []ProviderNodeView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	refs := make([]string, 0, len(s.views))
	for ref := range s.views {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	out := make([]ProviderNodeView, 0, len(refs))
	for _, ref := range refs {
		out = append(out, s.views[ref])
	}
	return out
}

// endpointFor resolves a reference to the complete service URL.
//
// This is the only path that yields an endpoint, and it is backend-only:
// nothing in the GUI layer calls it with data the user can forge, because
// the reference is minted here. The value it returns contains the
// access_token, so it must never be logged, marshalled or shown.
func (s *providerNodeStore) endpointFor(ref string) (string, error) {
	node, ok := s.nodeFor(ref)
	if !ok {
		return "", fmt.Errorf("%w: %q", errUnknownNodeRef, ref)
	}
	return node.Endpoint, nil
}

// nodeFor resolves a reference to the full runtime node.
func (s *providerNodeStore) nodeFor(ref string) (provider.Node, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.nodes[ref]
	return node, ok
}

// forget drops every published node. It exists so a caller that ends the
// session - or revokes consent - can drop the tokens it held rather than
// keeping them until the process exits.
func (s *providerNodeStore) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodes = make(map[string]provider.Node)
	s.views = make(map[string]ProviderNodeView)
	s.byID = make(map[string]string)
}

// newProviderNodeView projects one node onto the GUI-safe shape.
//
// The endpoint is parsed, never sliced. A parse failure yields an empty
// host and path rather than a prefix of the original string, because a
// prefix of a credential-bearing URL is still part of the credential.
func newProviderNodeView(ref string, node provider.Node) ProviderNodeView {
	view := ProviderNodeView{
		Ref:       ref,
		ID:        node.ID,
		Name:      node.Name,
		Scope:     string(node.Scope),
		Provider:  string(node.Provider),
		Ownership: string(node.Ownership),
		Discovery: string(node.Discovery),
	}
	if parsed, err := url.Parse(node.Endpoint); err == nil {
		view.Host = parsed.Host
		view.ServicePath = parsed.EscapedPath()
	}
	return view
}

// providerNodeDisplayHost is what the node picker shows. It reuses the
// view, so the GUI cannot accidentally render a different, unfiltered part
// of the endpoint.
func providerNodeDisplayHost(view ProviderNodeView) string {
	if host := strings.TrimSpace(view.Host); host != "" {
		return host
	}
	return view.ID
}
