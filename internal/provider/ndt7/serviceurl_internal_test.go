package ndt7

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

// The SDK (github.com/m-lab/ndt7-client-go v0.10.1) compares the service
// URL path literally. Its own constants live in the module's internal
// package (ndt7-client-go/internal/params) and cannot be imported here, so
// the two accepted paths are pinned verbatim in this test:
//
//	params.DownloadURLPath = "/ndt/v7/download"
//	params.UploadURLPath   = "/ndt/v7/upload"
//
// A path that is not exactly one of those two makes the SDK refuse the
// run with ErrServiceUnsupported ("unsupported service url").
const (
	sdkDownloadPath = "/ndt/v7/download"
	sdkUploadPath   = "/ndt/v7/upload"
)

// fakeToken is a made-up credential. No test in this package ever contacts
// a real M-Lab server, so a real access_token is never needed and never
// appears in a test.
const fakeToken = "fake-access-token.value"

// TestServiceURLNormalisesThePath is the table-driven contract for the
// URL the adapter hands to the SDK.
//
// Every host name and token below is fictional. The test is fully offline:
// it never opens a socket, never sleeps and never depends on scheduling.
func TestServiceURLNormalisesThePath(t *testing.T) {
	cases := []struct {
		name      string
		server    string
		scheme    string
		direction Direction
		want      string
		wantPath  string
	}{
		{
			name:      "bare host download",
			server:    "ndt.example.test",
			direction: DirectionDownload,
			want:      "wss://ndt.example.test/ndt/v7/download",
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "bare host upload",
			server:    "ndt.example.test",
			direction: DirectionUpload,
			want:      "wss://ndt.example.test/ndt/v7/upload",
			wantPath:  sdkUploadPath,
		},
		{
			name:      "bare host with explicit ws scheme",
			server:    "ndt.example.test",
			scheme:    "ws",
			direction: DirectionDownload,
			want:      "ws://ndt.example.test/ndt/v7/download",
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "bare host with port",
			server:    "ndt.example.test:443",
			direction: DirectionDownload,
			want:      "wss://ndt.example.test:443/ndt/v7/download",
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "base url",
			server:    "wss://ndt.example.test",
			direction: DirectionDownload,
			want:      "wss://ndt.example.test/ndt/v7/download",
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "base url with trailing slash",
			server:    "wss://ndt.example.test/",
			direction: DirectionUpload,
			want:      "wss://ndt.example.test/ndt/v7/upload",
			wantPath:  sdkUploadPath,
		},
		{
			name:      "https base url is upgraded to wss",
			server:    "https://ndt.example.test",
			direction: DirectionDownload,
			want:      "wss://ndt.example.test/ndt/v7/download",
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "full locate url download",
			server:    "wss://ndt.example.test/ndt/v7/download",
			direction: DirectionDownload,
			want:      "wss://ndt.example.test/ndt/v7/download",
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "full locate url upload",
			server:    "wss://ndt.example.test/ndt/v7/upload",
			direction: DirectionUpload,
			want:      "wss://ndt.example.test/ndt/v7/upload",
			wantPath:  sdkUploadPath,
		},
		{
			name:      "full locate url with access token",
			server:    "wss://ndt.example.test/ndt/v7/download?access_token=" + fakeToken,
			direction: DirectionDownload,
			want:      "wss://ndt.example.test/ndt/v7/download?access_token=" + fakeToken,
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "full locate url with port and several query parameters",
			server:    "wss://ndt.example.test:8443/ndt/v7/download?access_token=" + fakeToken + "&index=0&locate_version=v2",
			direction: DirectionDownload,
			want:      "wss://ndt.example.test:8443/ndt/v7/download?access_token=" + fakeToken + "&index=0&locate_version=v2",
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "path of the other direction is normalised to the current one",
			server:    "wss://ndt.example.test/ndt/v7/upload?access_token=" + fakeToken,
			direction: DirectionDownload,
			want:      "wss://ndt.example.test/ndt/v7/download?access_token=" + fakeToken,
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "upload run normalises a download path",
			server:    "wss://ndt.example.test/ndt/v7/download?access_token=" + fakeToken,
			direction: DirectionUpload,
			want:      "wss://ndt.example.test/ndt/v7/upload?access_token=" + fakeToken,
			wantPath:  sdkUploadPath,
		},
		{
			name:      "unrelated path is replaced, not appended to",
			server:    "wss://ndt.example.test/some/other/path",
			direction: DirectionDownload,
			want:      "wss://ndt.example.test/ndt/v7/download",
			wantPath:  sdkDownloadPath,
		},
		{
			name:      "https full locate url keeps the wss scheme",
			server:    "https://ndt.example.test/ndt/v7/download?access_token=" + fakeToken,
			scheme:    "wss",
			direction: DirectionDownload,
			want:      "wss://ndt.example.test/ndt/v7/download?access_token=" + fakeToken,
			wantPath:  sdkDownloadPath,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &Adapter{options: Options{
				Server:    tc.server,
				Scheme:    tc.scheme,
				Direction: tc.direction,
			}}

			got, err := adapter.serviceURL()
			if err != nil {
				t.Fatalf("serviceURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("serviceURL = %q, want %q", got, tc.want)
			}

			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatalf("parse result %q: %v", got, err)
			}

			// The path must be exactly the path the SDK accepts, with
			// no duplicated /ndt/v7/<direction> segment.
			if parsed.Path != tc.wantPath {
				t.Fatalf("path = %q, want %q", parsed.Path, tc.wantPath)
			}
			if got != tc.want {
				t.Fatalf("url = %q, want %q", got, tc.want)
			}
			if count := strings.Count(parsed.Path, "/ndt/v7/"); count != 1 {
				t.Fatalf("path %q contains %d /ndt/v7/ segments, want exactly 1", parsed.Path, count)
			}
			if count := strings.Count(got, "/ndt/v7/"); count != 1 {
				t.Fatalf("url %q contains %d /ndt/v7/ segments, want exactly 1", got, count)
			}
			if parsed.Path != sdkDownloadPath && parsed.Path != sdkUploadPath {
				t.Fatalf("path %q is not a path the SDK accepts", parsed.Path)
			}
			// The SDK reads the scheme from the service URL, so the
			// scheme must be the websocket one the adapter promised.
			if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
				t.Fatalf("scheme = %q, want ws or wss", parsed.Scheme)
			}
			if parsed.Host == "" {
				t.Fatal("host is empty")
			}
		})
	}
}

// TestServiceURLPreservesHostPortAndQuery checks the parts of a full
// Locate URL that the adapter must not touch: the host, the port and the
// whole query string, including the access_token.
func TestServiceURLPreservesHostPortAndQuery(t *testing.T) {
	const (
		host = "ndt.example.test"
		port = "8443"
	)
	query := "access_token=" + fakeToken + "&index=0&locate_version=v2"
	server := "wss://" + host + ":" + port + "/ndt/v7/download?" + query

	adapter := &Adapter{options: Options{Server: server, Direction: DirectionDownload}}
	got, err := adapter.serviceURL()
	if err != nil {
		t.Fatalf("serviceURL: %v", err)
	}
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result %q: %v", got, err)
	}
	if parsed.Host != host+":"+port {
		t.Fatalf("host = %q, want %q", parsed.Host, host+":"+port)
	}
	if parsed.RawQuery != query {
		t.Fatalf("query = %q, want %q", parsed.RawQuery, query)
	}
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if values.Get("access_token") != fakeToken {
		t.Fatalf("access_token = %q, want %q", values.Get("access_token"), fakeToken)
	}
	if values.Get("index") != "0" || values.Get("locate_version") != "v2" {
		t.Fatalf("query parameters were not preserved: %v", values)
	}
}

// TestServiceURLRejectsInvalidInput pins the existing error contract: an
// unparsable server address fails with the "ndt7: invalid server" error
// and the wrapped url.Error.
func TestServiceURLRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name   string
		server string
	}{
		{"unterminated ipv6 host", "wss://[::1"},
		// An invalid escape in the path is what makes url.Parse fail; a
		// bad escape in the query alone is accepted by net/url.
		{"invalid percent escape in path", "wss://ndt.example.test/ndt%zz/v7/download?access_token=" + fakeToken},
		{"control character", "wss://ndt.example.test/\x7f"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := &Adapter{options: Options{Server: tc.server, Direction: DirectionDownload}}
			got, err := adapter.serviceURL()
			if err == nil {
				t.Fatalf("serviceURL = %q, want an error", got)
			}
			if got != "" {
				t.Fatalf("serviceURL returned %q together with an error", got)
			}
			if !strings.Contains(err.Error(), "ndt7: invalid server") {
				t.Fatalf("error = %v, want it to mention %q", err, "ndt7: invalid server")
			}
			var urlErr *url.Error
			if !errors.As(err, &urlErr) {
				t.Fatalf("error = %v, want it to wrap a *url.Error", err)
			}
		})
	}
}

// TestServiceURLErrorDoesNotLeakCredentials makes sure the error path does
// not print the access_token of a Locate URL. The message reaches
// provider.FinalResult.ErrorMessage, which is displayed and persisted.
func TestServiceURLErrorDoesNotLeakCredentials(t *testing.T) {
	const secret = "fake-secret-token-value"
	// The invalid escape sits in the path, because net/url accepts a bad
	// escape in the query and would then parse the address successfully.
	cases := []string{
		"wss://ndt.example.test/ndt%zz/v7/download?access_token=" + secret,
		"wss://user:password@ndt.example.test/ndt%zz/v7/download?access_token=" + secret,
	}

	for _, server := range cases {
		adapter := &Adapter{options: Options{Server: server, Direction: DirectionDownload}}
		_, err := adapter.serviceURL()
		if err == nil {
			t.Fatalf("serviceURL(%q) did not fail", server)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaks the credential", err)
		}
		if strings.Contains(err.Error(), "password") {
			t.Fatalf("error %q leaks the userinfo", err)
		}
	}
}

// TestRedactServerForLog covers the helper that keeps credentials out of
// the error message.
func TestRedactServerForLog(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "query is removed",
			in:   "wss://ndt.example.test/ndt/v7/download?access_token=" + fakeToken,
			want: "wss://ndt.example.test/ndt/v7/download",
		},
		{
			name: "userinfo is removed",
			in:   "wss://user:password@ndt.example.test/ndt/v7/download",
			want: "wss://ndt.example.test/ndt/v7/download",
		},
		{
			name: "fragment is removed",
			in:   "wss://ndt.example.test/ndt/v7/download#secret",
			want: "wss://ndt.example.test/ndt/v7/download",
		},
		{
			name: "clean address is left alone",
			in:   "wss://ndt.example.test/ndt/v7/download",
			want: "wss://ndt.example.test/ndt/v7/download",
		},
		{
			name: "empty address stays empty",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactServerForLog(tc.in); got != tc.want {
				t.Fatalf("redactServerForLog(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNewRejectsUnsupportedDirection pins the contract that an
// unsupported direction is refused by the constructor, so serviceURL can
// never be reached with a direction the SDK does not have a path for.
func TestNewRejectsUnsupportedDirection(t *testing.T) {
	for _, direction := range []Direction{"", "sideways", "Download"} {
		if _, err := New(Options{Server: "ndt.example.test", Direction: direction}); err == nil {
			t.Fatalf("New accepted direction %q", direction)
		}
	}
}
