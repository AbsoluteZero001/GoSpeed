package nodes

import "testing"

func TestNetworkScope(t *testing.T) {
	cases := []struct {
		baseURL string
		want    string
	}{
		{"http://127.0.0.1:8080", ScopeLocal},
		{"http://localhost:8080", ScopeLocal},
		{"http://[::1]:8080", ScopeLocal},
		{"http://192.168.1.10:8080", ScopeLAN},
		{"http://10.0.0.5", ScopeLAN},
		{"http://8.8.8.8", ScopeRemote},
		{"https://speed.example.com", ScopeUnknown},
		{"", ScopeUnknown},
	}
	for _, testCase := range cases {
		if got := NetworkScope(testCase.baseURL); got != testCase.want {
			t.Errorf("NetworkScope(%q) = %q, want %q", testCase.baseURL, got, testCase.want)
		}
	}
}
