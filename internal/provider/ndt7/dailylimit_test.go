package ndt7_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider"
	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7/mock"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
)

// unusableStore stands in for a counter file that cannot be read or
// written. The runner must refuse to measure rather than measure without a
// limit, so this is a security property and not a mock convenience.
type unusableStore struct{ err error }

func (s unusableStore) Used(time.Time) (int, error)    { return 0, s.err }
func (s unusableStore) Reserve(time.Time) (int, error) { return 0, s.err }

// dayAt returns a fixed instant on the given local calendar day. Every test
// passes the instant it wants to count against, so no test has to wait for
// midnight and none of them can pass by accident at a day boundary.
func dayAt(t *testing.T, y int, m time.Month, d, hour int) time.Time {
	t.Helper()
	return time.Date(y, m, d, hour, 30, 0, 0, time.Local)
}

// TestDailyCounterSeparatesCalendarDays is the cross-day property. A run at
// 23:50 and a run at 00:10 the next morning must not add up.
func TestDailyCounterSeparatesCalendarDays(t *testing.T) {
	store := ndt7.NewMemoryDailyRunStore()
	late := dayAt(t, 2026, time.October, 22, 23)
	early := late.Add(40 * time.Minute) // 23:30 -> 00:10 the next day

	for i := 0; i < ndt7.MaxDailyRuns; i++ {
		if _, err := store.Reserve(late); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
	}
	used, err := store.Used(late)
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != ndt7.MaxDailyRuns {
		t.Fatalf("late-day count = %d, want %d", used, ndt7.MaxDailyRuns)
	}
	used, err = store.Used(early)
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 0 {
		t.Fatalf("the next calendar day starts at %d, want 0", used)
	}
	if _, err := store.Reserve(early); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if used, err = store.Used(late); err != nil || used != ndt7.MaxDailyRuns {
		t.Fatalf("the previous day changed to %d (err=%v)", used, err)
	}
}

// TestDailyCounterUsesTheLocalCalendarDay pins which midnight resets the
// counter. A UTC-based day would reset at 08:00 for a client in UTC+8,
// which is not the boundary a person means by "four tests a day".
func TestDailyCounterUsesTheLocalCalendarDay(t *testing.T) {
	store := ndt7.NewMemoryDailyRunStore()
	zone := time.FixedZone("UTC+8", 8*60*60)
	// 2026-10-22 23:30 in UTC+8 is 15:30 UTC on the same date.
	evening := time.Date(2026, time.October, 22, 23, 30, 0, 0, zone)
	if _, err := store.Reserve(evening); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	// The same instant expressed in UTC still belongs to that local day, so
	// the count must not depend on how the caller happens to carry the time.
	sameInstant := evening.UTC()
	used, err := store.Used(sameInstant)
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 1 {
		t.Fatalf("count for the same instant = %d, want 1", used)
	}
	// 2026-10-23 00:30 local is a new day even though it is the previous
	// UTC date.
	nextDay := evening.Add(time.Hour)
	used, err = store.Used(nextDay)
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 0 {
		t.Fatalf("the next local day starts at %d, want 0", used)
	}
}

// TestDailyCounterPersistsAcrossProcesses is the property that makes the
// limit more than an in-process formality: a second store instance over the
// same file - which is what a relaunched application is - sees the count the
// first one left behind.
func TestDailyCounterPersistsAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndt7-daily-runs.json")
	day := dayAt(t, 2026, time.October, 22, 12)

	first := ndt7.NewFileDailyRunStore(path)
	for i := 0; i < 2; i++ {
		if _, err := first.Reserve(day); err != nil {
			t.Fatalf("Reserve: %v", err)
		}
	}

	second := ndt7.NewFileDailyRunStore(path)
	used, err := second.Used(day)
	if err != nil {
		t.Fatalf("Used after restart: %v", err)
	}
	if used != 2 {
		t.Fatalf("count after restart = %d, want 2 - relaunching must not reset the limit", used)
	}
	if got := second.Reserve; got == nil {
		t.Fatal("Reserve must be callable on a reloaded store")
	}
	if _, err := second.Reserve(day); err != nil {
		t.Fatalf("Reserve after restart: %v", err)
	}
	third := ndt7.NewFileDailyRunStore(path)
	if used, err = third.Used(day); err != nil || used != 3 {
		t.Fatalf("count after two restarts = %d (err=%v), want 3", used, err)
	}
}

// TestDailyCounterKeepsTodayAndDropsStaleDays checks that the file cannot
// grow without bound and that pruning never touches the current day.
func TestDailyCounterKeepsTodayAndDropsStaleDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndt7-daily-runs.json")
	store := ndt7.NewFileDailyRunStore(path)
	old := dayAt(t, 2026, time.October, 1, 9)
	today := dayAt(t, 2026, time.October, 22, 9)

	if _, err := store.Reserve(old); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := store.Reserve(today); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := store.Reserve(today); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	var doc struct {
		Version int            `json:"version"`
		Days    map[string]int `json:"days"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode counter: %v", err)
	}
	if doc.Version == 0 {
		t.Fatal("the counter file must record its schema version")
	}
	if doc.Days["2026-10-01"] != 0 {
		t.Fatalf("a day far outside the retention window survived: %v", doc.Days)
	}
	if doc.Days["2026-10-22"] != 2 {
		t.Fatalf("today's count was pruned or lost: %v", doc.Days)
	}
}

// TestDailyCounterKeepsRecentPastDays guards the other half of pruning: a
// day inside the window must survive, otherwise a user who measured
// yesterday could be handed a fresh allowance that no longer matches what
// M-Lab recorded.
func TestDailyCounterKeepsRecentPastDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndt7-daily-runs.json")
	store := ndt7.NewFileDailyRunStore(path)
	recent := dayAt(t, 2026, time.October, 20, 9)
	today := dayAt(t, 2026, time.October, 22, 9)

	if _, err := store.Reserve(recent); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if _, err := store.Reserve(today); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	var doc struct {
		Days map[string]int `json:"days"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode counter: %v", err)
	}
	if doc.Days["2026-10-20"] != 1 {
		t.Fatalf("a recent day was pruned: %v", doc.Days)
	}
}

// TestDailyCounterRefusesUnreadableStateFailsClosed covers a corrupt or
// foreign counter file. Replacing it with an empty counter would erase the
// very number the limit depends on, so the store refuses and the runner
// refuses with it.
func TestDailyCounterRefusesUnreadableStateFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"not JSON", "this is not a counter"},
		{"no version", `{"days":{"2026-10-22":3}}`},
		{"future version", `{"version":99,"days":{}}`},
		{"empty file", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ndt7-daily-runs.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("seed counter: %v", err)
			}
			store := ndt7.NewFileDailyRunStore(path)
			if _, err := store.Used(time.Now()); !errors.Is(err, ndt7.ErrDailyRunLimitUnavailable) {
				t.Fatalf("Used err = %v, want ErrDailyRunLimitUnavailable", err)
			}
			if _, err := store.Reserve(time.Now()); !errors.Is(err, ndt7.ErrDailyRunLimitUnavailable) {
				t.Fatalf("Reserve err = %v, want ErrDailyRunLimitUnavailable", err)
			}
			// The file must be left exactly as it was, not repaired into an
			// empty counter that would silently lift the limit.
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read counter: %v", err)
			}
			if string(raw) != tc.content {
				t.Fatalf("the unreadable counter was rewritten to %q", raw)
			}
		})
	}
}

// TestDailyCounterReserveIsAtomic is the concurrency property: a burst of
// simultaneous reservations must count each one exactly once, because the
// runner's check-then-reserve sequence is only safe if Reserve is.
func TestDailyCounterReserveIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndt7-daily-runs.json")
	store := ndt7.NewFileDailyRunStore(path)
	day := dayAt(t, 2026, time.October, 22, 20)

	const goroutines = 32
	var wg sync.WaitGroup
	results := make([]int, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			used, err := store.Reserve(day)
			if err != nil {
				t.Errorf("Reserve: %v", err)
				return
			}
			results[idx] = used
		}(i)
	}
	wg.Wait()

	used, err := store.Used(day)
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != goroutines {
		t.Fatalf("count = %d, want %d", used, goroutines)
	}
	reloaded := ndt7.NewFileDailyRunStore(path)
	if used, err = reloaded.Used(day); err != nil || used != goroutines {
		t.Fatalf("reloaded count = %d (err=%v), want %d", used, err, goroutines)
	}
}

// TestDailyCounterFileHoldsNoCredential states what the counter keeps: dates
// and counts. A diagnostic bundle that ships the configuration directory
// must not carry a service URL out of it.
func TestDailyCounterFileHoldsNoCredential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ndt7-daily-runs.json")
	store := ndt7.NewFileDailyRunStore(path)
	if _, err := store.Reserve(dayAt(t, 2026, time.October, 22, 8)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	text := string(raw)
	for _, forbidden := range []string{"access_token", "wss://", "measurement-lab.org", "measurementlab.net"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the daily counter file carries %q: %s", forbidden, text)
		}
	}
}

// TestRunnerRefusesTheFifthMeasurementOfTheDay is the enforcement test: the
// ceiling is applied by the runner, not by a GUI that might forget.
func TestRunnerRefusesTheFifthMeasurementOfTheDay(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := rawRunner(t)
	endpoint := "ws://" + srv.Host()
	for i := 0; i < ndt7.MaxDailyRuns; i++ {
		req := downloadRequest(endpoint, "daily")
		if _, err := r.AuthorizeRun(req.Node.ID, provider.DirectionDownload); err != nil {
			t.Fatalf("AuthorizeRun %d: %v", i, err)
		}
		if _, err := r.Run(context.Background(), req); errors.Is(err, ndt7.ErrDailyRunLimitReached) {
			t.Fatalf("run %d was refused by the daily limit too early", i+1)
		}
	}

	req := downloadRequest(endpoint, "daily")
	if _, err := r.AuthorizeRun(req.Node.ID, provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	result, err := r.Run(context.Background(), req)
	if !errors.Is(err, ndt7.ErrDailyRunLimitReached) {
		t.Fatalf("the %dth run err = %v, want ErrDailyRunLimitReached", ndt7.MaxDailyRuns+1, err)
	}
	if result.Status != provider.StatusFailed || result.ErrorCode != "daily_limit_reached" {
		t.Fatalf("result = status %q code %q, want failed/daily_limit_reached", result.Status, result.ErrorCode)
	}
	if result.Download != nil {
		t.Fatal("a run refused by the daily limit must publish no transfer")
	}
	if got := srv.Snapshot().Connections; got != int64(ndt7.MaxDailyRuns) {
		t.Fatalf("the server saw %d connections, want exactly %d", got, ndt7.MaxDailyRuns)
	}
}

// TestDailyLimitRefusalKeepsTheConfirmation: the ceiling is not the user's
// decision, so a run it stops must not spend the confirmation the user just
// gave - otherwise a quota would silently require two confirmations.
func TestDailyLimitRefusalKeepsTheConfirmation(t *testing.T) {
	r := rawRunner(t)
	counter := ndt7.NewMemoryDailyRunStore()
	r.SetDailyRunStore(counter)
	now := time.Now()
	for i := 0; i < ndt7.MaxDailyRuns; i++ {
		if _, err := counter.Reserve(now); err != nil {
			t.Fatalf("seed Reserve: %v", err)
		}
	}
	if _, err := r.AuthorizeRun("over-quota", provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	_, err := r.Run(context.Background(), downloadRequest("ws://127.0.0.1:1", "over-quota"))
	if !errors.Is(err, ndt7.ErrDailyRunLimitReached) {
		t.Fatalf("Run err = %v, want ErrDailyRunLimitReached", err)
	}
	if got := r.PendingRunAuthorizations(); got != 1 {
		t.Fatalf("a quota refusal consumed the confirmation: %d pending", got)
	}
	if used, err := counter.Used(now); err != nil || used != ndt7.MaxDailyRuns {
		t.Fatalf("a refused run changed the day's count to %d (err=%v)", used, err)
	}
}

// TestDailyLimitCountsOnlyRunsThatStarted pins the counting stage. A run
// refused for a missing authorization never reached the reserve step, so it
// must not appear in the day's count.
func TestDailyLimitCountsOnlyRunsThatStarted(t *testing.T) {
	counter := ndt7.NewMemoryDailyRunStore()
	r := rawRunner(t)
	r.SetDailyRunStore(counter)

	for i := 0; i < 5; i++ {
		if _, err := r.Run(context.Background(), downloadRequest("ws://127.0.0.1:1", "unstarted")); !errors.Is(err, ndt7.ErrRunAuthorizationRequired) {
			t.Fatalf("attempt %d: err = %v, want ErrRunAuthorizationRequired", i, err)
		}
	}
	used, err := counter.Used(time.Now())
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used != 0 {
		t.Fatalf("five refused runs counted %d measurement(s), want 0", used)
	}
}

// TestRunnerFailsClosedWhenTheCounterIsUnavailable: a limit that cannot be
// read is not a limit. The runner must refuse the run rather than measure
// without one.
func TestRunnerFailsClosedWhenTheCounterIsUnavailable(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := rawRunner(t)
	r.SetDailyRunStore(unusableStore{err: ndt7.ErrDailyRunLimitUnavailable})
	req := downloadRequest("ws://"+srv.Host(), "no-counter")
	if _, err := r.AuthorizeRun(req.Node.ID, provider.DirectionDownload); err != nil {
		t.Fatalf("AuthorizeRun: %v", err)
	}
	result, err := r.Run(context.Background(), req)
	if !errors.Is(err, ndt7.ErrDailyRunLimitUnavailable) {
		t.Fatalf("Run err = %v, want ErrDailyRunLimitUnavailable", err)
	}
	if result.ErrorCode != "daily_limit_unavailable" {
		t.Fatalf("error code = %q, want daily_limit_unavailable", result.ErrorCode)
	}
	assertNoTraffic(t, srv)
}

// TestConcurrentRunsNeverExceedTheDailyCeiling is the concurrency test for
// the whole gate: eight simultaneous authorized runs against one client must
// never produce more than MaxDailyRuns transfers.
//
// Which attempt loses depends on the scheduler, so the test asserts the
// invariant rather than a schedule: the number of runs that were not refused
// stays within the ceiling, and the day's count never falls below it once
// the ceiling has been reached.
func TestConcurrentRunsNeverExceedTheDailyCeiling(t *testing.T) {
	srv := mock.New(mock.ScenarioDownload, 0)
	defer srv.Close()

	r := rawRunner(t)
	counter := ndt7.NewMemoryDailyRunStore()
	r.SetDailyRunStore(counter)

	const attempts = ndt7.MaxDailyRuns * 2
	outcomes := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := downloadRequest("ws://"+srv.Host(), "concurrent-daily")
			if _, err := r.AuthorizeRun(req.Node.ID, provider.DirectionDownload); err != nil {
				outcomes <- err
				return
			}
			_, err := r.Run(context.Background(), req)
			outcomes <- err
		}()
	}
	wg.Wait()
	close(outcomes)

	started := 0
	for err := range outcomes {
		switch {
		case err == nil:
			started++
		case errors.Is(err, ndt7.ErrDailyRunLimitReached):
			// stopped before the socket
		default:
			t.Fatalf("unexpected error from a concurrent run: %v", err)
		}
	}
	if started > ndt7.MaxDailyRuns {
		t.Fatalf("%d of %d concurrent runs started, at most %d may", started, attempts, ndt7.MaxDailyRuns)
	}
	if started == 0 {
		t.Fatalf("none of %d concurrent runs was allowed through", attempts)
	}
	if got := srv.Snapshot().Connections; got > int64(ndt7.MaxDailyRuns) {
		t.Fatalf("the server saw %d connections, at most %d may have started", got, ndt7.MaxDailyRuns)
	}
	used, err := counter.Used(time.Now())
	if err != nil {
		t.Fatalf("Used: %v", err)
	}
	if used < ndt7.MaxDailyRuns || used > attempts {
		t.Fatalf("the day counted %d measurement(s), want between %d and %d",
			used, ndt7.MaxDailyRuns, attempts)
	}
}

// TestDefaultDailyRunStorePathIsPerUser checks the default location is a
// per-user configuration path, not something inside the working directory
// that a cleanup or a second checkout would erase.
func TestDefaultDailyRunStorePathIsPerUser(t *testing.T) {
	path, err := ndt7.DefaultDailyRunStorePath()
	if err != nil {
		t.Skipf("no per-user configuration directory on this machine: %v", err)
	}
	if !strings.Contains(path, "GoSpeed") || !strings.HasSuffix(path, "ndt7-daily-runs.json") {
		t.Fatalf("default counter path = %q", path)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("default counter path %q is not absolute", path)
	}
}
