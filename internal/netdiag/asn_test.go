package netdiag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// asnFixture is a local stand-in for the RIPEstat API so every assertion runs
// without touching a third party.
type asnFixture struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests int
	holderOK bool
}

func newASNFixture(t *testing.T) *asnFixture {
	t.Helper()
	f := &asnFixture{holderOK: true}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		holderOK := f.holderOK
		f.mu.Unlock()

		switch {
		case strings.Contains(r.URL.Path, "network-info"):
			_, _ = w.Write([]byte(`{"prefix":"93.184.216.0/24","asns":[15133]}`))
		case strings.Contains(r.URL.Path, "as-overview"):
			if !holderOK {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(`{"holder":"Example Networks"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *asnFixture) enricher() *RIPEstatEnricher {
	e := NewRIPEstatEnricher()
	e.client = &http.Client{Timeout: 2 * time.Second}
	e.base = f.server.URL
	return e
}

func (f *asnFixture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func (f *asnFixture) setHolderOK(ok bool) {
	f.mu.Lock()
	f.holderOK = ok
	f.mu.Unlock()
}

func TestEnricherResolvesASNumberAndHolder(t *testing.T) {
	fixture := newASNFixture(t)
	got, err := fixture.enricher().Enrich(context.Background(), []string{"93.184.216.34"})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].ASN != 15133 {
		t.Errorf("asn = %d, want 15133", got[0].ASN)
	}
	if got[0].Holder != "Example Networks" {
		t.Errorf("holder = %q", got[0].Holder)
	}
	if got[0].Prefix != "93.184.216.0/24" {
		t.Errorf("prefix = %q", got[0].Prefix)
	}
	if got[0].Source != ASNSource {
		t.Errorf("source = %q, want %q so a reader knows where this came from", got[0].Source, ASNSource)
	}
}

// TestEnricherDisclaimsThePath is the rule that matters most for an attribution:
// knowing who owns an address says nothing about who forwarded the packet.
func TestEnricherDisclaimsThePath(t *testing.T) {
	fixture := newASNFixture(t)
	got, err := fixture.enricher().Enrich(context.Background(), []string{"93.184.216.34"})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if got[0].Detail == "" {
		t.Fatal("an attribution must carry a caveat")
	}
	if !strings.Contains(got[0].Detail, "not evidence") {
		t.Errorf("detail %q does not disclaim the forwarding path", got[0].Detail)
	}
}

func TestEnricherCachesAnswers(t *testing.T) {
	fixture := newASNFixture(t)
	e := fixture.enricher()
	if _, err := e.Enrich(context.Background(), []string{"93.184.216.34"}); err != nil {
		t.Fatalf("first Enrich: %v", err)
	}
	afterFirst := fixture.count()

	if _, err := e.Enrich(context.Background(), []string{"93.184.216.34"}); err != nil {
		t.Fatalf("second Enrich: %v", err)
	}
	if fixture.count() != afterFirst {
		t.Errorf("a cached answer still made %d extra requests", fixture.count()-afterFirst)
	}
}

func TestEnricherExpiresCachedAnswers(t *testing.T) {
	fixture := newASNFixture(t)
	e := fixture.enricher()
	if _, err := e.Enrich(context.Background(), []string{"93.184.216.34"}); err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	afterFirst := fixture.count()

	clock := time.Now()
	e.now = func() time.Time { return clock.Add(asnCacheTTL + time.Minute) }

	if _, err := e.Enrich(context.Background(), []string{"93.184.216.34"}); err != nil {
		t.Fatalf("second Enrich: %v", err)
	}
	if fixture.count() == afterFirst {
		t.Error("an expired answer was reused instead of being refreshed")
	}
}

func TestEnricherRateLimitsRequests(t *testing.T) {
	fixture := newASNFixture(t)
	e := fixture.enricher()
	slept := 0
	e.sleep = func(ctx context.Context, d time.Duration) error {
		slept++
		return nil
	}

	// Four distinct public addresses, none cacheable.
	_, err := e.Enrich(context.Background(), []string{
		"93.184.216.34", "8.8.8.8", "1.1.1.1", "9.9.9.9",
	})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if slept == 0 {
		t.Error("the limiter never slept, so requests were not spaced")
	}
}

func TestEnricherRateLimiterHonoursCancellation(t *testing.T) {
	fixture := newASNFixture(t)
	e := fixture.enricher()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }

	if _, err := e.Enrich(ctx, []string{"93.184.216.34", "8.8.8.8"}); err == nil {
		t.Error("a cancelled enrichment must report the cancellation")
	}
}

func TestEnricherSurvivesProviderFailure(t *testing.T) {
	fixture := newASNFixture(t)
	fixture.setHolderOK(false)

	got, err := fixture.enricher().Enrich(context.Background(), []string{"93.184.216.34", "8.8.8.8"})
	if err != nil {
		t.Fatalf("a provider failure must not be a hard error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want one per address", len(got))
	}
	// The AS number survives even when the holder lookup fails.
	if got[0].ASN != 15133 {
		t.Errorf("asn = %d, want the resolved value to survive", got[0].ASN)
	}
	if got[0].Holder != Unknown {
		t.Errorf("holder = %q, want unknown after a failure", got[0].Holder)
	}
	if got[0].Detail == "" {
		t.Error("a partial answer must explain what was missing")
	}
}

func TestEnricherNeverContactsProviderForPrivateAddresses(t *testing.T) {
	fixture := newASNFixture(t)
	got, err := fixture.enricher().Enrich(context.Background(), []string{
		"192.168.1.1", "10.0.0.1", "127.0.0.1", "169.254.1.1",
	})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %+v, want nothing", got)
	}
	if fixture.count() != 0 {
		t.Errorf("the provider was contacted %d times for private addresses", fixture.count())
	}
}

func TestStaticEnricherIsDeterministic(t *testing.T) {
	e := StaticEnricher{Answers: map[string]ASInfo{
		"93.184.216.34": {Address: "93.184.216.34", ASN: 15133, Holder: "Example Networks", Source: "static"},
	}}
	got, err := e.Enrich(context.Background(), []string{"93.184.216.34", "8.8.8.8"})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2", len(got))
	}
	if got[0].ASN != 15133 {
		t.Errorf("asn = %d", got[0].ASN)
	}
	if got[1].ASN != 0 || got[1].Detail == "" {
		t.Errorf("an address with no configured answer must say so: %+v", got[1])
	}
}
