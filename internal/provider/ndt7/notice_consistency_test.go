package ndt7_test

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
)

// TestConsentNoticeNamesTheEnforcedLimits ties the disclosure to the
// behaviour.
//
// Each fact below is enforced somewhere in this package, and the notice is
// the only place a user is told about it before they agree. Asserting the
// wording here means a limit cannot be added, removed or tightened in code
// without the disclosure following it, which is the failure mode a privacy
// notice actually has.
func TestConsentNoticeNamesTheEnforcedLimits(t *testing.T) {
	notice := ndt7.NewRunner().ConsentNotice()

	facts := map[string]string{
		"the wording version":              "ndt7-notice-v3",
		"consent does not authorise a run": "does NOT authorise a measurement",
		"the per-run authorization":        "per-run authorization is not remembered",
		"the daily ceiling":                "at most " + strconv.Itoa(ndt7.MaxDailyRuns) + " NDT7 measurements per calendar day",
		"a cancelled run still counts":     "still counts against the daily limit",
		"what is kept locally":             "What GoSpeed keeps on this computer",
		"the Locate request is external":   "itself an external request",
		"the M-Lab privacy policy":         "https://www.measurementlab.net/privacy/",
		"the M-Lab AUP":                    "https://www.measurementlab.net/aup/",
	}
	for what, needle := range facts {
		if !strings.Contains(notice, needle) {
			t.Fatalf("the notice does not state %s: it must contain %q", what, needle)
		}
	}
}

// TestNoticeVersionTracksConsentVersion is the same coupling from the other
// side: the record and the text must name the same version, or a stored
// agreement could be matched against wording the user never saw.
func TestNoticeVersionTracksConsentVersion(t *testing.T) {
	r := ndt7.NewRunner()
	notice := r.ConsentNotice()

	// An empty policy version defaults to the version in effect, and it is
	// accepted - so the version in force is reachable only through that
	// default.
	if err := r.RecordConsent(true, time.Now(), ""); err != nil {
		t.Fatalf("RecordConsent with the default version: %v", err)
	}
	// The previous wording must no longer open the gate: a stored agreement
	// to an older notice has to be re-taken.
	superseded := ndt7.NewRunner()
	if err := superseded.RecordConsent(true, time.Now(), "ndt7-notice-v2"); err == nil {
		t.Fatal("an agreement to the previous wording was accepted; the notice change would be silent")
	}
	if !strings.Contains(notice, "ndt7-notice-v3") {
		t.Fatalf("the notice does not name the version it is filed under")
	}
}

// TestPrivacyPolicyStatesTheSameFacts checks PRIVACY.md against the
// behaviour the provider implements. The policy is the document a reader
// actually consults, and it promises things in a different language from
// the notice the program shows; without this check the two drift apart
// silently.
func TestPrivacyPolicyStatesTheSameFacts(t *testing.T) {
	const path = "../../../PRIVACY.md"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	policy := string(raw)

	facts := map[string]string{
		"the daily ceiling":            "每日不超过 **4 次**",
		"the per-run authorization":    "单次测速授权",
		"persisted discovery consent":  "节点发现同意",
		"Locate exposes the public IP": "Locate",
		"the counter survives restart": "重启 GoSpeed 不会清零",
		"what is stored locally":       "GoSpeed 在你的本机",
		"the M-Lab privacy policy":     "https://www.measurementlab.net/privacy/",
		"the M-Lab AUP":                "https://www.measurementlab.net/aup/",
	}
	for what, needle := range facts {
		if !strings.Contains(policy, needle) {
			t.Fatalf("PRIVACY.md does not state %s: it must contain %q", what, needle)
		}
	}
	// The policy and the notice must agree on the number, not merely both
	// mention a limit.
	if !strings.Contains(policy, strconv.Itoa(ndt7.MaxDailyRuns)) {
		t.Fatalf("PRIVACY.md does not mention the enforced ceiling %d", ndt7.MaxDailyRuns)
	}
	// A superseded wording must not be presented as current.
	if strings.Contains(policy, "ndt7-notice-2") {
		t.Fatal("PRIVACY.md pins a superseded notice version")
	}
}

// TestExperimentalNoticeAdmitsItsGaps keeps the experimental module honest.
//
// The mlab-poc client is a separate module with its own notice. If that
// notice ever described the production per-run authorization or the daily
// limit, it would be promising protections its code does not have.
func TestExperimentalNoticeAdmitsItsGaps(t *testing.T) {
	const path = "../../../experiments/mlab-poc/provider/consent.go"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(raw)
	for _, needle := range []string{
		`ConsentNoticeVersion = "gospeed-mlab-notice-3"`,
		"没有**实现正式产品中的两项保护",
		"每日不超过 4 次测试",
	} {
		if !strings.Contains(source, needle) {
			t.Fatalf("the experimental notice no longer states %q", needle)
		}
	}
}

// TestNoAuthorizationErrorIsEverReportedAsAConsentError keeps the two gates
// distinguishable. A caller that matched on ErrConsentRequired alone would
// treat "you did not confirm this run" as "you never agreed at all", and
// could answer it by re-showing the durable consent dialog - which is
// exactly the shortcut this design forbids.
func TestNoAuthorizationErrorIsEverReportedAsAConsentError(t *testing.T) {
	if errors.Is(ndt7.ErrRunAuthorizationRequired, ndt7.ErrConsentRequired) {
		t.Fatal("a missing per-run authorization must not match the durable consent sentinel")
	}
	if errors.Is(ndt7.ErrRunAuthorizationMismatch, ndt7.ErrConsentRequired) {
		t.Fatal("an authorization mismatch must not match the durable consent sentinel")
	}
}
