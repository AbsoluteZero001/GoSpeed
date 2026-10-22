package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
)

// locateShapedToken is a fabricated credential. Nothing under test parses or
// validates it; it exists so a leak assertion has something specific to
// find.
const locateShapedToken = "eyJhbGciOiJub25lIn0.eyJleHAiOjk5OTk5OTk5OTl9.not-a-real-signature"

// discoveredNodes is the shape a Locate answer produces: two third-party
// nodes whose endpoint is the COMPLETE service URL, token included, exactly
// as the ndt7 provider stores it.
func discoveredNodes() []provider.Node {
	return []provider.Node{
		{
			ID:        "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org",
			Name:      "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org (Hong Kong, HK)",
			Endpoint:  "wss://ndt-mlab2-hkg03.mlab-oti.measurement-lab.org:443/ndt/v7/download?access_token=" + locateShapedToken + "&index=0&locate_version=v2",
			Scope:     provider.ScopeRemote,
			Provider:  provider.IDMLabNDT7,
			Ownership: provider.OwnershipThirdParty,
			Discovery: provider.DiscoveryService,
		},
		{
			ID:        "ndt-mlab3-fra04.mlab-oti.measurement-lab.org",
			Name:      "ndt-mlab3-fra04.mlab-oti.measurement-lab.org (Frankfurt, DE)",
			Endpoint:  "wss://ndt-mlab3-fra04.mlab-oti.measurement-lab.org:443/ndt/v7/download?access_token=" + locateShapedToken + "&index=0&locate_version=v2",
			Scope:     provider.ScopeRemote,
			Provider:  provider.IDMLabNDT7,
			Ownership: provider.OwnershipThirdParty,
			Discovery: provider.DiscoveryService,
		},
	}
}

// TestProviderNodeViewFieldSetIsFixed is the structural guarantee.
//
// It does not look for a credential string in the output; it pins the shape
// of the type. A field that could carry a URL, a query or a token cannot be
// added to ProviderNodeView without this test failing, which is the point:
// the boundary is enforced by the type, not by a filter that a future field
// would slip past.
func TestProviderNodeViewFieldSetIsFixed(t *testing.T) {
	want := []string{
		"discovery",
		"host",
		"id",
		"name",
		"ownership",
		"provider",
		"ref",
		"scope",
		"servicePath",
	}
	typ := reflect.TypeOf(ProviderNodeView{})
	got := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Fatalf("field %q must declare a JSON name; an untagged field can be serialised by accident", field.Name)
		}
		name := strings.Split(tag, ",")[0]
		lower := strings.ToLower(name)
		for _, forbidden := range []string{"endpoint", "token", "url", "query", "secret", "credential"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("field %q (%q) can carry a credential", field.Name, name)
			}
		}
		got = append(got, name)
	}
	sortStrings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProviderNodeView JSON field set = %v, want %v", got, want)
	}
}

// TestProviderNodeViewJSONCarriesNoCredential checks the serialised output
// of a real discovery answer. It walks the decoded document rather than
// matching one string, so a credential hidden in a nested value, an escaped
// form, or a field this test has never heard of would still be found.
func TestProviderNodeViewJSONCarriesNoCredential(t *testing.T) {
	store := newProviderNodeStore()
	views := store.publish(discoveredNodes())

	raw, err := json.Marshal(views)
	if err != nil {
		t.Fatalf("marshal views: %v", err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal views: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("decoded %d views, want 2", len(decoded))
	}
	for _, found := range collectStringPaths(decoded) {
		for _, forbidden := range []string{
			locateShapedToken,
			"access_token",
			"locate_version",
			"wss://",
			"index=",
		} {
			if strings.Contains(found.value, forbidden) {
				t.Fatalf("%s carries %q: %q", found.key, forbidden, found.value)
			}
		}
	}
	// The raw bytes are checked as well: an escaped form would survive the
	// decoded walk.
	if strings.Contains(string(raw), locateShapedToken) || strings.Contains(string(raw), "access_token") {
		t.Fatalf("the serialised payload leaks the credential: %s", raw)
	}
}

// TestProviderNodeViewKeepsUsefulDisplayFields is the other half of the
// boundary: dropping the credential must not drop what the node picker
// needs to tell two nodes apart.
func TestProviderNodeViewKeepsUsefulDisplayFields(t *testing.T) {
	store := newProviderNodeStore()
	views := store.publish(discoveredNodes())

	first := views[0]
	if first.ID != "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org" {
		t.Fatalf("ID = %q", first.ID)
	}
	if first.Name != "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org (Hong Kong, HK)" {
		t.Fatalf("Name = %q", first.Name)
	}
	if first.Host != "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org:443" {
		t.Fatalf("Host = %q", first.Host)
	}
	if first.ServicePath != "/ndt/v7/download" {
		t.Fatalf("ServicePath = %q", first.ServicePath)
	}
	if first.Scope != string(provider.ScopeRemote) || first.Provider != string(provider.IDMLabNDT7) {
		t.Fatalf("classification lost: scope=%q provider=%q", first.Scope, first.Provider)
	}
	if first.Ownership != string(provider.OwnershipThirdParty) || first.Discovery != string(provider.DiscoveryService) {
		t.Fatalf("classification lost: ownership=%q discovery=%q", first.Ownership, first.Discovery)
	}
	if first.Ref == "" {
		t.Fatal("Ref must not be empty: it is the handle the GUI names the node by")
	}
	if views[0].Ref == views[1].Ref {
		t.Fatal("two published nodes must get distinct references")
	}
	if host := providerNodeDisplayHost(first); host != "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org:443" {
		t.Fatalf("display host = %q", host)
	}
}

// TestBackendExecutionPathStillGetsTheFullEndpoint is the counterpart: the
// GUI cannot hold a token, but the measurement still has to reach one.
func TestBackendExecutionPathStillGetsTheFullEndpoint(t *testing.T) {
	nodes := discoveredNodes()
	store := newProviderNodeStore()
	views := store.publish(nodes)

	endpoint, err := store.endpointFor(views[0].Ref)
	if err != nil {
		t.Fatalf("endpointFor: %v", err)
	}
	if endpoint != nodes[0].Endpoint {
		t.Fatalf("the backend lost the endpoint:\n got %q\nwant %q", endpoint, nodes[0].Endpoint)
	}
	// The token really is there, which is why it must not have crossed the
	// boundary in the view.
	if !strings.Contains(endpoint, "access_token="+locateShapedToken) {
		t.Fatalf("the resolved endpoint has no token, so the measurement could not authenticate: %q", endpoint)
	}
	node, ok := store.nodeFor(views[1].Ref)
	if !ok || node.ID != nodes[1].ID {
		t.Fatalf("nodeFor(%q) = %+v, %v", views[1].Ref, node, ok)
	}
}

// TestForgedReferenceResolvesNothing: the reference is the only thing the
// frontend sends back, so a value it invented must not reach an endpoint.
func TestForgedReferenceResolvesNothing(t *testing.T) {
	store := newProviderNodeStore()
	views := store.publish(discoveredNodes())

	for _, ref := range []string{
		"",
		"pnode-0",
		"pnode-9999",
		"ndt-mlab2-hkg03.mlab-oti.measurement-lab.org",
		"../pnode-1",
		strings.ToUpper(views[0].Ref),
	} {
		if _, err := store.endpointFor(ref); !errors.Is(err, errUnknownNodeRef) {
			t.Fatalf("endpointFor(%q) err = %v, want errUnknownNodeRef", ref, err)
		}
	}
}

// TestRefreshedDiscoverySupersedesTheOldTokens: an access_token is short
// lived, so a new answer must replace the previous nodes rather than merge
// with them, and a reference minted for the old token must stop resolving.
func TestRefreshedDiscoverySupersedesTheOldTokens(t *testing.T) {
	store := newProviderNodeStore()
	first := store.publish(discoveredNodes())
	second := store.publish(discoveredNodes()[:1])

	if _, err := store.endpointFor(first[0].Ref); !errors.Is(err, errUnknownNodeRef) {
		t.Fatalf("a superseded reference still resolves: %v", err)
	}
	if _, err := store.endpointFor(second[0].Ref); err != nil {
		t.Fatalf("the current reference does not resolve: %v", err)
	}
	if got := len(store.allViews()); got != 1 {
		t.Fatalf("the store kept %d views after a refresh, want 1", got)
	}
	if first[1].Ref == second[0].Ref {
		t.Fatal("a refreshed answer reused a reference; the GUI could not tell the answers apart")
	}
}

// TestForgetDropsEveryToken covers consent revocation and session end: the
// store must be able to drop what it holds rather than keep tokens until the
// process exits.
func TestForgetDropsEveryToken(t *testing.T) {
	store := newProviderNodeStore()
	views := store.publish(discoveredNodes())
	store.forget()

	for _, view := range views {
		if _, err := store.endpointFor(view.Ref); !errors.Is(err, errUnknownNodeRef) {
			t.Fatalf("endpointFor(%q) after forget err = %v, want errUnknownNodeRef", view.Ref, err)
		}
	}
	if got := store.allViews(); len(got) != 0 {
		t.Fatalf("forget left %d views", len(got))
	}
}

// TestMalformedEndpointNeverLeaksThroughTheHostField: the host is derived by
// parsing, so an endpoint this code cannot parse yields an empty host rather
// than a truncated copy of a credential-bearing string.
func TestMalformedEndpointNeverLeaksThroughTheHostField(t *testing.T) {
	unparseable := []string{
		"://not a url?access_token=" + locateShapedToken,
		locateShapedToken + "@ndt.example.invalid/path",
		"\x00wss://host?access_token=" + locateShapedToken,
		"wss://ho st/?access_token=" + locateShapedToken,
	}
	for _, endpoint := range unparseable {
		view := newProviderNodeView("pnode-x", provider.Node{ID: "id", Endpoint: endpoint})
		if view.Host != "" {
			t.Fatalf("Host = %q for endpoint %q; an unparseable endpoint must not yield a host", view.Host, endpoint)
		}
		if got := providerNodeDisplayHost(view); got != "id" {
			t.Fatalf("display host fallback = %q, want the node id", got)
		}
	}

	// An endpoint that DOES parse is not a problem by itself - the problem
	// would be the parts of it that reach the GUI. A bad escape in the query
	// is tolerated by net/url, so this case parses; only the credential
	// matters.
	for _, endpoint := range []string{
		"wss://host/ndt/v7/download?access_token=" + locateShapedToken + "%zz",
		"wss://user:" + locateShapedToken + "@host/ndt/v7/download",
		"wss://host/ndt/v7/download?access_token=" + locateShapedToken,
	} {
		view := newProviderNodeView("pnode-x", provider.Node{ID: "id", Endpoint: endpoint})
		if strings.Contains(view.Host, locateShapedToken) || strings.Contains(view.ServicePath, locateShapedToken) {
			t.Fatalf("the projection leaked part of the endpoint: %+v", view)
		}
		raw, err := json.Marshal(view)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(raw), locateShapedToken) {
			t.Fatalf("a parsed endpoint leaked through the view: %s", raw)
		}
	}
}

// TestStoreFormattingDoesNotLeakTheEndpoint checks the log-shaped outputs:
// a %+v of a view, of the whole view list, and of an error must be safe,
// because those are exactly what a developer console or a log line prints.
func TestStoreFormattingDoesNotLeakTheEndpoint(t *testing.T) {
	store := newProviderNodeStore()
	views := store.publish(discoveredNodes())

	_, err := store.endpointFor("pnode-does-not-exist")
	if err == nil {
		t.Fatal("expected an error for an unknown reference")
	}
	if strings.Contains(fmt.Sprintf("%+v", err), locateShapedToken) {
		t.Fatalf("the error carries the credential: %v", err)
	}

	for _, text := range []string{
		fmt.Sprintf("%+v", views),
		fmt.Sprintf("%+v", store.allViews()),
		fmt.Sprintf("%v", views[0]),
		fmt.Sprintf("%#v", views[0]),
	} {
		if strings.Contains(text, locateShapedToken) || strings.Contains(text, "access_token") {
			t.Fatalf("formatting a view leaks the credential: %s", text)
		}
	}
}

// TestProviderNodeDisplayHostFallsBackToID covers the shape a UI shows when
// an endpoint could not be parsed.
func TestProviderNodeDisplayHostFallsBackToID(t *testing.T) {
	if got := providerNodeDisplayHost(ProviderNodeView{ID: "node", Host: "   "}); got != "node" {
		t.Fatalf("display host = %q, want node", got)
	}
	if got := providerNodeDisplayHost(ProviderNodeView{ID: "node", Host: "example.invalid:443"}); got != "example.invalid:443" {
		t.Fatalf("display host = %q", got)
	}
}

// sortStrings keeps the field-set comparison order independent of the
// order reflection happens to report.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

type stringPath struct {
	key   string
	value string
}

// collectStringPaths walks a decoded JSON document and returns every string
// value with the key it was found under. Comparing against one forbidden
// substring would miss a credential split across two fields, or one the
// encoder escaped.
func collectStringPaths(value any) []stringPath {
	var out []stringPath
	var walk func(key string, value any)
	walk = func(key string, value any) {
		switch typed := value.(type) {
		case map[string]any:
			for k, v := range typed {
				walk(k, v)
			}
		case []any:
			for i, v := range typed {
				walk(fmt.Sprintf("%s[%d]", key, i), v)
			}
		case string:
			out = append(out, stringPath{key: key, value: typed})
		}
	}
	walk("$", value)
	return out
}
