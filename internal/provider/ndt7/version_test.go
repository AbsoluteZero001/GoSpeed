package ndt7_test

import (
	"strings"
	"testing"

	ndt7client "github.com/m-lab/ndt7-client-go"

	"github.com/AbsoluteZero001/GoSpeed/internal/provider/ndt7"
	"github.com/AbsoluteZero001/GoSpeed/internal/version"
)

// TestUserAgentCarriesTheProductVersion pins the client version that the
// M-Lab measurement publishes.
//
// The metadata M-Lab records includes the client version. A provider that
// carried its own copy of that version would publish a stale one and would
// drift from the version the GUI and the CLI report, so the constant comes
// from the project's single version source and there is no second copy.
func TestUserAgentCarriesTheProductVersion(t *testing.T) {
	if ndt7.ClientVersion != version.Version {
		t.Fatalf("client version = %q, want the product version %q", ndt7.ClientVersion, version.Version)
	}
	agent := ndt7client.MakeUserAgent(ndt7.ClientName, ndt7.ClientVersion)
	if !strings.Contains(agent, version.Version) {
		t.Fatalf("the User-Agent %q must carry the product version %q", agent, version.Version)
	}
	if !strings.Contains(agent, ndt7.ClientName) {
		t.Fatalf("the User-Agent %q must name the client", agent)
	}
}
