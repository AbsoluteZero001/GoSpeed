package ndt7

// The M-Lab Acceptable Use Policy asks software clients not to run more
// than four tests per day:
//
//	"Automated software or hardware clients should not run more than 4
//	 tests per day, and test times should be randomized."
//	 -- https://www.measurementlab.net/aup/ (policy page, "Automated
//	    clients" section)
//
// GoSpeed takes the conservative reading: at most four NDT7 measurements
// per client instance per calendar day, counted by the backend, enforced
// before the WebSocket is opened.
//
// Why a persisted counter and not an in-memory one
//
// A limit the user can reset by quitting and relaunching the application is
// not a limit. An in-memory counter would satisfy every functional test and
// still be trivially defeated, and it would also under-report against M-Lab,
// whose accounting is per client, not per process. The counter therefore
// lives in a small JSON file in the user's configuration directory.
//
// Why the counter is reserved late
//
// The count is taken at the last gate before the transfer starts, not when
// the run is requested. A run refused for a malformed request, refused for
// lack of authorization, or cancelled before it opened a socket never
// reaches the reserve step and therefore never counts. Conversely a run that
// did open a socket counts even if it later failed, because M-Lab recorded
// it whether or not GoSpeed obtained a rate.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// MaxDailyRuns is the number of NDT7 measurements one client instance
	// may start on one calendar day. It is the AUP recommendation for
	// automated clients, used verbatim rather than interpreted.
	MaxDailyRuns = 4

	// dailyCounterRetainedDays bounds the counter file. Only the current
	// day's count decides anything, and an entry for a past day can never
	// become today's, so dropping old entries cannot lift the limit.
	dailyCounterRetainedDays = 8

	// dailyCounterVersion is the on-disk schema version. A file written by
	// a different version is refused rather than guessed at: silently
	// resetting the count would defeat the limit it stores.
	dailyCounterVersion = 1
)

// ErrDailyRunLimitReached is returned by Run when this client instance has
// already started MaxDailyRuns measurements today.
var ErrDailyRunLimitReached = errors.New("ndt7: this client has reached the daily measurement limit; the M-Lab Acceptable Use Policy asks software clients not to run more than 4 tests per day")

// ErrDailyRunLimitUnavailable is returned when the counter cannot be read or
// written and the limit therefore cannot be enforced.
//
// The runner fails closed: an unenforced limit would let the client exceed
// the AUP recommendation silently, which is the worse outcome.
var ErrDailyRunLimitUnavailable = errors.New("ndt7: the daily measurement counter is unavailable, so the AUP daily limit cannot be enforced")

// DailyRunStore counts the measurements one client instance started per
// calendar day.
//
// It is an interface so that tests never touch the user's real counter file
// and so that a future GUI can place the counter in its own storage. Both
// methods take the instant to count against, which makes day boundaries
// testable without waiting for one.
type DailyRunStore interface {
	// Used reports how many measurements were already started on the
	// calendar day of now. It does not change the counter.
	Used(now time.Time) (int, error)
	// Reserve records one started measurement and returns the number
	// started on the calendar day of now, including this one. Concurrent
	// calls must each count exactly once.
	Reserve(now time.Time) (int, error)
}

// DefaultDailyRunStorePath returns the per-user counter file.
//
// The file lives next to the rest of the GoSpeed configuration so that it
// is removed with it and is never mistaken for a measurement record.
func DefaultDailyRunStorePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
	}
	return filepath.Join(dir, "GoSpeed", "ndt7-daily-runs.json"), nil
}

// dayKey formats the calendar day of t in t's own location.
//
// The day boundary is therefore the user's local midnight, which is the
// boundary a person means by "per day". It deliberately does not use UTC:
// a client in UTC+8 would otherwise reset at 08:00 local time.
func dayKey(t time.Time) string {
	return t.Format("2006-01-02")
}

// dailyCounterFile is the persisted document. It holds dates and counts
// only: no endpoint, no token, no host.
type dailyCounterFile struct {
	Version int            `json:"version"`
	Days    map[string]int `json:"days"`
}

// MemoryDailyRunStore is an in-memory DailyRunStore.
//
// It is not a loophole around the limit: it is the store the tests use, and
// it is also the correct choice for a caller that supplies its own durable
// storage. Production builds leave it unset and get FileDailyRunStore.
type MemoryDailyRunStore struct {
	mu   sync.Mutex
	days map[string]int
}

// NewMemoryDailyRunStore returns an empty in-memory counter.
func NewMemoryDailyRunStore() *MemoryDailyRunStore {
	return &MemoryDailyRunStore{days: make(map[string]int)}
}

// Used implements DailyRunStore.
func (s *MemoryDailyRunStore) Used(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.days[dayKey(now)], nil
}

// Reserve implements DailyRunStore.
func (s *MemoryDailyRunStore) Reserve(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.days == nil {
		s.days = make(map[string]int)
	}
	day := dayKey(now)
	s.days[day]++
	return s.days[day], nil
}

// FileDailyRunStore is the persisted DailyRunStore used by production
// builds. It loads the counter on first use and rewrites it atomically, so
// an interrupted write cannot leave a truncated file that would be read as
// "no measurements today".
type FileDailyRunStore struct {
	mu   sync.Mutex
	path string
	days map[string]int
	// loadErr remembers a read or parse failure. It is returned on every
	// later call instead of retrying into the same broken state, and the
	// caller fails closed.
	loadErr error
	loaded  bool
}

// NewFileDailyRunStore returns a counter stored at path. The file is not
// read until the first call, so constructing the store never touches the
// filesystem.
func NewFileDailyRunStore(path string) *FileDailyRunStore {
	return &FileDailyRunStore{path: path}
}

// Path returns the file the store persists to.
func (s *FileDailyRunStore) Path() string { return s.path }

// Used implements DailyRunStore.
func (s *FileDailyRunStore) Used(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return 0, err
	}
	return s.days[dayKey(now)], nil
}

// Reserve implements DailyRunStore.
func (s *FileDailyRunStore) Reserve(now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadLocked(); err != nil {
		return 0, err
	}
	day := dayKey(now)
	s.days[day]++
	s.pruneLocked(now)
	if err := s.saveLocked(); err != nil {
		// The in-memory increment is rolled back so a failed write cannot
		// leave the process believing it has room it does not have.
		s.days[day]--
		return 0, err
	}
	return s.days[day], nil
}

// loadLocked reads the counter file once.
func (s *FileDailyRunStore) loadLocked() error {
	if s.loaded {
		return s.loadErr
	}
	s.loaded = true
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.days = make(map[string]int)
		return nil
	}
	if err != nil {
		s.loadErr = fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
		return s.loadErr
	}
	var file dailyCounterFile
	if err := json.Unmarshal(raw, &file); err != nil {
		// A corrupt or foreign file is not silently replaced by an empty
		// counter: that would erase the count the limit depends on.
		s.loadErr = fmt.Errorf("%w: the counter file at %s is not readable as a GoSpeed daily counter", ErrDailyRunLimitUnavailable, s.path)
		return s.loadErr
	}
	if file.Version != dailyCounterVersion {
		s.loadErr = fmt.Errorf("%w: the counter file at %s has version %d, this build writes version %d",
			ErrDailyRunLimitUnavailable, s.path, file.Version, dailyCounterVersion)
		return s.loadErr
	}
	s.days = file.Days
	if s.days == nil {
		s.days = make(map[string]int)
	}
	return nil
}

// pruneLocked drops entries older than the retention window. Today's entry
// is never dropped.
func (s *FileDailyRunStore) pruneLocked(now time.Time) {
	cutoff := now.AddDate(0, 0, -dailyCounterRetainedDays)
	stale := make([]string, 0)
	for day := range s.days {
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil || parsed.Before(cutoff) {
			stale = append(stale, day)
		}
	}
	for _, day := range stale {
		delete(s.days, day)
	}
}

// saveLocked rewrites the counter through a temporary file and a rename, so
// a reader never observes a partially written document.
func (s *FileDailyRunStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
	}
	days := make(map[string]int, len(s.days))
	for day, count := range s.days {
		days[day] = count
	}
	raw, err := json.MarshalIndent(dailyCounterFile{Version: dailyCounterVersion, Days: days}, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
	}
	raw = append(raw, '\n')
	// The file records dates and counts. The mode is restrictive anyway so
	// that a per-user diagnostic dump cannot surprise anyone.
	temp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
	}
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
	}
	if err := os.Rename(tempName, s.path); err != nil {
		return fmt.Errorf("%w: %v", ErrDailyRunLimitUnavailable, err)
	}
	return nil
}
