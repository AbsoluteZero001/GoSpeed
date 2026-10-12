package provider

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// This file covers the Registry: registration, lookup, duplicate handling and
// lifecycle. The registry must never reach the network, never probe a node and
// never hold measurement session state.

// stubRunner is a Runner that records what was asked of it, so a test can prove
// the registry itself never triggers a measurement or a negotiation.
type stubRunner struct {
	id ID

	mu           sync.Mutex
	negotiations int
	runs         int
}

func (s *stubRunner) ID() ID { return s.id }

func (s *stubRunner) Negotiate(context.Context, Node) (CapabilitySet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.negotiations++
	return CapabilitySet{}, nil
}

func (s *stubRunner) Run(context.Context, RunRequest) (FinalResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs++
	return FinalResult{ProviderID: s.id}, nil
}

func (s *stubRunner) counts() (negotiations, runs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.negotiations, s.runs
}

// newGoSpeedRegistryForTest builds a registry holding the only provider this
// build is authorised to use. It is the in-package mirror of the desktop's
// default wiring and performs no network I/O.
func newGoSpeedRegistryForTest() (*Registry, error) {
	registry := NewRegistry()
	measurement := NewGoSpeedHTTP(nil)
	if err := registry.Register(IDGoSpeedHTTP, measurement, func() error {
		measurement.CloseIdleConnections()
		return nil
	}); err != nil {
		return nil, err
	}
	return registry, nil
}

func TestRegistryLooksUpByProviderID(t *testing.T) {
	registry := NewRegistry()
	runner := &stubRunner{id: IDGoSpeedHTTP}
	if err := registry.Register(IDGoSpeedHTTP, runner, nil); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := registry.Lookup(IDGoSpeedHTTP)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got.ID() != IDGoSpeedHTTP {
		t.Fatalf("Lookup returned %q", got.ID())
	}
	if !registry.Has(IDGoSpeedHTTP) {
		t.Error("Has reported a registered provider as missing")
	}
}

func TestRegistryRefusesDuplicateRegistration(t *testing.T) {
	registry := NewRegistry()
	first := &stubRunner{id: IDGoSpeedHTTP}
	if err := registry.Register(IDGoSpeedHTTP, first, nil); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	second := &stubRunner{id: IDGoSpeedHTTP}
	err := registry.Register(IDGoSpeedHTTP, second, nil)
	if !errors.Is(err, ErrProviderAlreadyRegistered) {
		t.Fatalf("error = %v, want ErrProviderAlreadyRegistered", err)
	}

	// The original runner must still be the registered one.
	got, err := registry.Lookup(IDGoSpeedHTTP)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != Runner(first) {
		t.Fatal("a refused duplicate registration replaced the live runner")
	}
}

func TestRegistryReportsUnregisteredProvider(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(IDGoSpeedHTTP, &stubRunner{id: IDGoSpeedHTTP}, nil)

	for _, id := range []ID{IDMLabNDT7, IDCloudflare, ID("unknown-provider")} {
		got, err := registry.Lookup(id)
		if !errors.Is(err, ErrProviderNotRegistered) {
			t.Errorf("Lookup(%q) error = %v, want ErrProviderNotRegistered", id, err)
		}
		if got != nil {
			t.Errorf("Lookup(%q) returned a runner; the registry must never substitute one", id)
		}
		if registry.Has(id) {
			t.Errorf("Has(%q) reported an unregistered provider as present", id)
		}
	}
}

// TestRegistryNeverStartsAProviderAction is the red line of registration:
// wiring a provider must not measure, probe or negotiate anything.
func TestRegistryNeverStartsAProviderAction(t *testing.T) {
	registry := NewRegistry()
	runner := &stubRunner{id: IDGoSpeedHTTP}
	if err := registry.Register(IDGoSpeedHTTP, runner, func() error { return nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := registry.Lookup(IDGoSpeedHTTP); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, err := registry.Lookup(IDCloudflare); err == nil {
		t.Fatal("expected a lookup failure for an unregistered provider")
	}
	_ = registry.IDs()

	negotiations, runs := runner.counts()
	if negotiations != 0 {
		t.Errorf("registering and looking up negotiated %d time(s)", negotiations)
	}
	if runs != 0 {
		t.Errorf("registering and looking up started %d measurement(s)", runs)
	}
}

func TestRegistryRejectsEmptyIDAndNilRunner(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("", &stubRunner{id: IDGoSpeedHTTP}, nil); !errors.Is(err, ErrNilID) {
		t.Errorf("empty id error = %v, want ErrNilID", err)
	}
	if err := registry.Register(IDGoSpeedHTTP, nil, nil); !errors.Is(err, ErrNilRunner) {
		t.Errorf("nil runner error = %v, want ErrNilRunner", err)
	}
	if len(registry.IDs()) != 0 {
		t.Error("a refused registration must not leave an entry behind")
	}
}

func TestRegistryCloseReleasesEachProviderOnce(t *testing.T) {
	registry := NewRegistry()
	var mu sync.Mutex
	closed := map[ID]int{}
	closer := func(id ID) Closer {
		return func() error {
			mu.Lock()
			defer mu.Unlock()
			closed[id]++
			return nil
		}
	}

	registry.MustRegister(IDGoSpeedHTTP, &stubRunner{id: IDGoSpeedHTTP}, closer(IDGoSpeedHTTP))
	registry.MustRegister(ID("second"), &stubRunner{id: ID("second")}, nil)

	if err := registry.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Closing twice must not release anything twice.
	if err := registry.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if closed[IDGoSpeedHTTP] != 1 {
		t.Fatalf("gospeed-http released %d times, want exactly 1", closed[IDGoSpeedHTTP])
	}
	if len(registry.IDs()) != 0 {
		t.Error("a closed registry still reports providers")
	}
	if _, err := registry.Lookup(IDGoSpeedHTTP); !errors.Is(err, ErrProviderNotRegistered) {
		t.Error("a closed registry still hands out runners")
	}
	if err := registry.Register(IDGoSpeedHTTP, &stubRunner{id: IDGoSpeedHTTP}, nil); err == nil {
		t.Error("a closed registry accepted a new registration")
	}
}

func TestRegistryCloseReportsFailures(t *testing.T) {
	registry := NewRegistry()
	boom := errors.New("close failed")
	registry.MustRegister(IDGoSpeedHTTP, &stubRunner{id: IDGoSpeedHTTP}, func() error { return boom })

	if err := registry.Close(); !errors.Is(err, boom) {
		t.Fatalf("Close error = %v, want the closer's error", err)
	}
}

func TestRegistryIDsAreStable(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(IDCloudflare, &stubRunner{id: IDCloudflare}, nil)
	registry.MustRegister(IDGoSpeedHTTP, &stubRunner{id: IDGoSpeedHTTP}, nil)

	first := registry.IDs()
	for i := 0; i < 5; i++ {
		again := registry.IDs()
		if len(again) != len(first) {
			t.Fatalf("IDs length changed: %d vs %d", len(again), len(first))
		}
		for index := range first {
			if again[index] != first[index] {
				t.Fatalf("IDs are not stable: %v vs %v", again, first)
			}
		}
	}
}

func TestRegistryIsSafeForConcurrentUse(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(IDGoSpeedHTTP, &stubRunner{id: IDGoSpeedHTTP}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if index%4 == 0 {
				_ = registry.Register(ID("extra"), &stubRunner{id: ID("extra")}, nil)
				return
			}
			if _, err := registry.Lookup(IDGoSpeedHTTP); err != nil {
				t.Errorf("Lookup: %v", err)
			}
			_ = registry.IDs()
			_ = registry.Has(IDGoSpeedHTTP)
		}(i)
	}
	wg.Wait()
}

// TestRegistryMustRegisterPanicsOnProgrammingErrors documents the difference
// between static wiring and runtime registration.
func TestRegistryMustRegisterPanicsOnProgrammingErrors(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(IDGoSpeedHTTP, &stubRunner{id: IDGoSpeedHTTP}, nil)

	defer func() {
		if recover() == nil {
			t.Fatal("MustRegister did not panic on a duplicate id")
		}
	}()
	registry.MustRegister(IDGoSpeedHTTP, &stubRunner{id: IDGoSpeedHTTP}, nil)
}

// TestDefaultRegistryRegistersOnlyGoSpeedHTTP pins the authorised provider set.
// A build must not silently gain an M-Lab or Cloudflare backend.
func TestDefaultRegistryRegistersOnlyGoSpeedHTTP(t *testing.T) {
	registry, err := newGoSpeedRegistryForTest()
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	ids := registry.IDs()
	if len(ids) != 1 || ids[0] != IDGoSpeedHTTP {
		t.Fatalf("registered providers = %v, want exactly [gospeed-http]", ids)
	}
	for _, id := range []ID{IDMLabNDT7, IDCloudflare} {
		if registry.Has(id) {
			t.Errorf("provider %q must not be registered in this build", id)
		}
	}
}

// TestRegistryLookedUpRunnerCanMeasure runs a real measurement through a runner
// that came out of a registry, against a local GoSpeed server.
func TestRegistryLookedUpRunnerCanMeasure(t *testing.T) {
	testServer := realGoSpeedServer(t)

	registry, err := newGoSpeedRegistryForTest()
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Close() })

	runner, err := registry.Lookup(IDGoSpeedHTTP)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	caps, err := runner.Negotiate(context.Background(), nodeFor(testServer.URL))
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := runner.Run(ctx, RunRequest{
		Node:         nodeFor(testServer.URL),
		Capabilities: caps,
		Budget:       BudgetLimits{DownloadBytes: measurableByteBudget},
	})
	if err != nil {
		t.Fatalf("Run: %v (%s)", err, result.ErrorMessage)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed", result.Status)
	}
	if result.Download == nil || result.Download.Mbps == nil {
		t.Fatal("a registry served runner produced no download rate")
	}
}
