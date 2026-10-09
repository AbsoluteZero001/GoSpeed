package nodes

import (
	"errors"
	"testing"
)

func TestValidateTargetAddressPolicy(t *testing.T) {
	cases := []struct {
		name    string
		node    Node
		wantErr bool
	}{
		{
			name: "loopback with local flag",
			node: Node{ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8080", Protocol: ProtocolHTTP, Local: true},
		},
		{
			name:    "loopback without local flag",
			node:    Node{ID: "local", Name: "Local", BaseURL: "http://127.0.0.1:8080", Protocol: ProtocolHTTP},
			wantErr: true,
		},
		{
			name:    "aws metadata address",
			node:    Node{ID: "meta", Name: "Meta", BaseURL: "http://169.254.169.254/latest", Protocol: ProtocolHTTP},
			wantErr: true,
		},
		{
			name:    "alibaba metadata address",
			node:    Node{ID: "meta", Name: "Meta", BaseURL: "http://100.100.100.200", Protocol: ProtocolHTTP},
			wantErr: true,
		},
		{
			name:    "metadata hostname",
			node:    Node{ID: "meta", Name: "Meta", BaseURL: "http://metadata.google.internal", Protocol: ProtocolHTTP},
			wantErr: true,
		},
		{
			name:    "link local address",
			node:    Node{ID: "link", Name: "Link", BaseURL: "http://169.254.10.10", Protocol: ProtocolHTTP},
			wantErr: true,
		},
		{
			name: "private LAN address",
			node: Node{ID: "lan", Name: "LAN", BaseURL: "http://192.168.1.20:8080", Protocol: ProtocolHTTP},
		},
		{
			name: "public address",
			node: Node{ID: "public", Name: "Public", BaseURL: "https://203.0.113.10", Protocol: ProtocolHTTPS},
		},
		{
			name: "host name",
			node: Node{ID: "host", Name: "Host", BaseURL: "https://speed.example.com", Protocol: ProtocolHTTPS},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.node.ValidateTarget()
			if testCase.wantErr {
				if !errors.Is(err, ErrUnsafeTarget) {
					t.Fatalf("error = %v, want ErrUnsafeTarget", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
