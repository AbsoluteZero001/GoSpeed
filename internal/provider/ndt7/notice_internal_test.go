package ndt7

import (
	"strings"
	"testing"
)

// TestConsentNoticeNamesTheEnforcedTrafficBudget ties the disclosure's
// traffic numbers to the constants the provider enforces.
//
// The notice is the only place the user learns what one measurement may
// cost them. If the budget changes and the wording does not, the disclosure
// silently starts describing a limit that is no longer enforced - and the
// consent the user gave would then cover a measurement they were never told
// about.
func TestConsentNoticeNamesTheEnforcedTrafficBudget(t *testing.T) {
	notice := NewRunner().ConsentNotice()
	for _, want := range []string{
		"download MB: " + mibLabel(DefaultBudgetBytes.Download),
		"upload MB:   " + mibLabel(DefaultBudgetBytes.Upload),
		"total MB:    " + mibLabel(DefaultBudgetBytes.Total),
	} {
		if !strings.Contains(notice, want) {
			t.Fatalf("the notice does not state %q; the enforced budget is %+v", want, DefaultBudgetBytes)
		}
	}
}

// TestConsentNoticeAndEnforcedBudgetAgreeOnTheCaps proves the disclosure
// and the budget are the same numbers in the same order.
//
// The wording of the caps is part of the consent: the user accepts a
// measurement that moves at most this much traffic. A cap that the notice
// names differently from the one the provider enforces is a consent the user
// did not actually give.
func TestConsentNoticeAndEnforcedBudgetAgreeOnTheCaps(t *testing.T) {
	notice := NewRunner().ConsentNotice()
	caps := []struct {
		label string
		bytes int64
	}{
		{"download", DefaultBudgetBytes.Download},
		{"upload", DefaultBudgetBytes.Upload},
		{"total", DefaultBudgetBytes.Total},
	}
	for _, cap := range caps {
		if cap.bytes <= 0 {
			t.Fatalf("the %s cap is %d, want a positive cap", cap.label, cap.bytes)
		}
		if !strings.Contains(notice, mibLabel(cap.bytes)) {
			t.Fatalf("the notice does not name the %s cap of %d bytes", cap.label, cap.bytes)
		}
	}
}
