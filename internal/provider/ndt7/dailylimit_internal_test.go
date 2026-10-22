package ndt7

import (
	"errors"
	"testing"
	"time"
)

// TestReserveRaceRefusesAndKeepsTheSlot pins the accounting rule for a run
// that passed the cheap pre-check and only then found the ceiling taken.
//
// The rule is deliberately kept here rather than in the black-box suite:
// producing that interleaving from concurrent goroutines depends on the
// scheduler, and a test that can only observe it occasionally is not a test.
// Calling the two steps directly is the deterministic way to state it.
func TestReserveRaceRefusesAndKeepsTheSlot(t *testing.T) {
	r := NewRunner()
	store := NewMemoryDailyRunStore()
	r.SetDailyRunStore(store)
	now := time.Now()

	for i := 0; i < MaxDailyRuns-1; i++ {
		if _, err := store.Reserve(now); err != nil {
			t.Fatalf("seed Reserve: %v", err)
		}
	}

	// This run's pre-check passes: one slot is still free.
	if err := r.checkDailyLimit(now); err != nil {
		t.Fatalf("pre-check refused a run with a free slot: %v", err)
	}
	// It reserves, and that is the fourth measurement of the day.
	if err := r.reserveDailyRun(now); err != nil {
		t.Fatalf("reserve refused the last free slot: %v", err)
	}

	// A second run that ran its pre-check before the ceiling was reached
	// now finds it gone.
	if err := r.checkDailyLimit(now); !errors.Is(err, ErrDailyRunLimitReached) {
		t.Fatalf("pre-check err = %v, want ErrDailyRunLimitReached", err)
	}

	// Its reservation fails, and the slot it took is NOT given back: the
	// counter may only be under-counted by a bug, never by a lost race.
	if err := r.reserveDailyRun(now); !errors.Is(err, ErrDailyRunLimitReached) {
		t.Fatalf("reserve err = %v, want ErrDailyRunLimitReached", err)
	}
	used, err := store.Used(now)
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != MaxDailyRuns+1 {
		t.Fatalf("the day counted %d, want %d: a refused reservation keeps its slot",
			used, MaxDailyRuns+1)
	}
}

// TestDailyStoreFailsClosedWithoutAUsableCounter checks the resolution path:
// with no store configured the runner reaches for the per-user file, and a
// configuration directory it cannot name is a refusal rather than an
// unlimited default.
func TestDailyStoreFailsClosedWithoutAUsableCounter(t *testing.T) {
	r := NewRunner()
	if r.dailyStore != nil {
		t.Fatal("a fresh runner must not hold a counter until one is needed")
	}
	store, err := r.dailyRunStore()
	if err != nil {
		if !errors.Is(err, ErrDailyRunLimitUnavailable) {
			t.Fatalf("err = %v, want ErrDailyRunLimitUnavailable", err)
		}
		return
	}
	// Where the platform does have a configuration directory, the resolved
	// store must be remembered so the file is opened once per runner.
	if r.dailyStore == nil {
		t.Fatal("the resolved store must be cached on the runner")
	}
	if again, err := r.dailyRunStore(); err != nil || again != store {
		t.Fatalf("dailyRunStore is not stable: %v %v", again, err)
	}
}
