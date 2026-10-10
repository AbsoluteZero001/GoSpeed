package mlabpoc

import (
	"strings"
	"testing"

	v2 "github.com/m-lab/locate/api/v2"
)

// P0-L security regression tests for the trusted-host suffix gate. The gate
// must only accept hosts that belong to the official M-Lab domains, must
// enforce full DNS label boundaries, and must not be bypassed by case
// changes, trailing dots, URL encoding, credentials, ports or crafted names.

var prodSuffixes = []string{"measurement-lab.org", "measurementlab.net"}

func TestHostAllowedLabelBoundary(t *testing.T) {
	testCases := []struct {
		name string
		host string
		want bool
	}{
		// Accepted: real M-Lab host shapes.
		{"exact platform domain", "measurement-lab.org", true},
		{"real node subdomain", "ndt-mlab2-hkg03.mlab-oti.measurement-lab.org", true},
		{"uppercase normalizes", "NDT.MLAB-OTI.MEASUREMENT-LAB.ORG", true},
		{"single trailing dot (FQDN)", "ndt.mlab-oti.measurement-lab.org.", true},
		{"second official suffix", "m1001.mlab-oti.measurementlab.net", true},
		{"loopback ip not under official suffixes", "127.0.0.1", false},

		// Rejected: forged suffix embedding and prefix confusion.
		{"suffix glued to prefix", "evilmeasurement-lab.org", false},
		{"suffix as middle label", "measurement-lab.org.evil.com", false},
		{"suffix at front", "measurement-lab.org.attacker.example", false},
		{"underscore lookalike", "measurement_lab.org", false},
		{"lookalike tld", "measurement-lab.org.evil.io", false},

		// Rejected: normalization abuse.
		{"double trailing dot", "ndt.measurement-lab.org..", false},
		{"interior space", "measurement-lab .org", false},
		{"fullwidth dot lookalike", "measurement-lab\u3002org", false},
		{"null byte", "ndt.measurement-lab.org\x00", false},
		{"control character", "ndt\x01.measurement-lab.org", false},
		{"empty host", "", false},
		{"ipv6 literal", "::1", false},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostAllowed(tc.host, prodSuffixes); got != tc.want {
				t.Fatalf("hostAllowed(%q) = %v, want %v", tc.host, got, tc.want)
			}
		})
	}
}

// trustedTarget builds a Locate target whose download/upload URLs both point
// at rawURL.
func trustedTarget(rawURL string) *v2.Target {
	return &v2.Target{
		Machine:  "machine",
		Hostname: "ndt.mlab-oti.measurement-lab.org",
		URLs: map[string]string{
			"wss:///ndt/v7/download": rawURL,
			"wss:///ndt/v7/upload":   strings.Replace(rawURL, "download", "upload", 1),
		},
	}
}

func TestValidateTargetRejectsBypassAttempts(t *testing.T) {
	testCases := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{
			name:   "real node URL passes",
			rawURL: "wss://ndt-mlab2-hkg03.mlab-oti.measurement-lab.org/ndt/v7/download?access_token=x",
		},
		{
			name:   "explicit 443 passes",
			rawURL: "wss://ndt.measurement-lab.org:443/ndt/v7/download",
		},
		{
			name:   "uppercase host passes",
			rawURL: "wss://NDT.MLAB-OTI.MEASUREMENT-LAB.ORG/ndt/v7/download",
		},
		{
			name:    "credentials in URL rejected",
			rawURL:  "wss://user@ndt.measurement-lab.org/ndt/v7/download",
			wantErr: true,
		},
		{
			name:    "userinfo decoy on attacker host rejected",
			rawURL:  "wss://measurement-lab.org@evil.example/ndt/v7/download",
			wantErr: true,
		},
		{
			name:    "nonstandard port rejected",
			rawURL:  "wss://ndt.measurement-lab.org:8443/ndt/v7/download",
			wantErr: true,
		},
		{
			name:    "insecure ws scheme rejected under wss allow list",
			rawURL:  "ws://ndt.measurement-lab.org/ndt/v7/download",
			wantErr: true,
		},
		{
			name:    "attacker host rejected",
			rawURL:  "wss://evil.example/ndt/v7/download",
			wantErr: true,
		},
		{
			// Go's net/url rejects percent-encoding of ASCII bytes that
			// must appear literally in a host ("invalid URL escape"), so
			// encoding tricks fail closed at parse time instead of
			// producing a different DNS name.
			name:    "percent-encoded ASCII in host fails parsing",
			rawURL:  "wss://measurement%2Dlab.org/ndt/v7/download",
			wantErr: true,
		},
		{
			name:    "percent-encoded dot fails parsing too",
			rawURL:  "wss://measurement%2Elab.org/ndt/v7/download",
			wantErr: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTarget(trustedTarget(tc.rawURL), DefaultLocateSchemes, prodSuffixes)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateTarget(%q) = nil, want error", tc.rawURL)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTarget(%q) = %v, want nil", tc.rawURL, err)
			}
		})
	}
}
