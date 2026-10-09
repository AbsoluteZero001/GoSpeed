package nodes

import "testing"

func TestNetworkScope(t *testing.T) {
	cases := []struct {
		baseURL string
		want    string
	}{
		{"http://127.0.0.1:8080", ScopeLocal},
		{"http://127.1.2.3:8080", ScopeLocal},
		{"http://localhost:8080", ScopeLocal},
		{"http://LOCALHOST:8080", ScopeLocal},
		{"http://[::1]:8080", ScopeLocal},
		{"http://192.168.1.10:8080", ScopeLAN},
		{"http://10.0.0.5", ScopeLAN},
		{"http://172.16.5.5", ScopeLAN},
		{"http://[fd00::1]:8080", ScopeLAN},
		{"http://[fe80::1]:8080", ScopeLAN},
		{"http://8.8.8.8", ScopeRemote},
		{"https://1.1.1.1", ScopeRemote},
		{"https://[2001:4860:4860::8888]", ScopeRemote},
		{"https://speed.example.com", ScopeUnknown},
		{"https://192.168.example.com", ScopeUnknown},
		{"", ScopeUnknown},
		{"not a url", ScopeUnknown},
	}
	for _, testCase := range cases {
		if got := NetworkScope(testCase.baseURL); got != testCase.want {
			t.Errorf("NetworkScope(%q) = %q, want %q", testCase.baseURL, got, testCase.want)
		}
	}
}

func TestScopeForHost(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"127.0.0.1", ScopeLocal},
		{"::1", ScopeLocal},
		{"localhost", ScopeLocal},
		{"localhost.", ScopeUnknown}, // a trailing dot is a different name; no guessing
		{"192.168.0.1", ScopeLAN},
		{"fe80::1", ScopeLAN},
		{"9.9.9.9", ScopeRemote},
		{"speed.example.com", ScopeUnknown},
		{"", ScopeUnknown},
	}
	for _, testCase := range cases {
		if got := ScopeForHost(testCase.host); got != testCase.want {
			t.Errorf("ScopeForHost(%q) = %q, want %q", testCase.host, got, testCase.want)
		}
	}
}

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.9.9.9", true},
		{"::1", true},
		{"localhost", true},
		{" LOCALHOST ", true},
		{"", false},
		{"localhost.localdomain", false},
		{"192.168.1.1", false},
		{"8.8.8.8", false},
		{"speed.example.com", false},
	}
	for _, testCase := range cases {
		if got := IsLoopbackHost(testCase.host); got != testCase.want {
			t.Errorf("IsLoopbackHost(%q) = %v, want %v", testCase.host, got, testCase.want)
		}
	}
}
