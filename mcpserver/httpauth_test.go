package mcpserver

import (
	"testing"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8484", true},
		{"127.0.0.1", true},
		{"localhost:8484", true},
		{"localhost", true},
		{"[::1]:8484", true},
		{"::1", true},
		{":8484", false}, // binds all interfaces
		{"0.0.0.0:8484", false},
		{"192.168.1.10:8484", false},
		{"example.com:8484", false},
	}
	for _, tc := range cases {
		if got := isLoopbackAddr(tc.addr); got != tc.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestValidateBindAddr(t *testing.T) {
	cases := []struct {
		name     string
		addr     string
		authMode string
		insecure bool
		wantErr  bool
	}{
		{"loopback none ok", "127.0.0.1:8484", authModeNone, false, false},
		{"non-loopback none refused", ":8484", authModeNone, false, true},
		{"non-loopback none insecure ok", "0.0.0.0:8484", authModeNone, true, false},
		{"non-loopback bearer ok", "0.0.0.0:8484", authModeBearer, false, false},
		{"non-loopback oauth ok", "0.0.0.0:8484", authModeOAuth, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBindAddr(tc.addr, tc.authMode, tc.insecure)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateBindAddr(%q, %q, %v) err = %v, wantErr %v", tc.addr, tc.authMode, tc.insecure, err, tc.wantErr)
			}
		})
	}
}

func TestResourceMetadataURL(t *testing.T) {
	cases := []struct {
		audience string
		want     string
	}{
		{"https://api.example.com/mcp", "https://api.example.com/.well-known/oauth-protected-resource"},
		{"https://api.example.com", "https://api.example.com/.well-known/oauth-protected-resource"},
		{"api-resource", "api-resource/.well-known/oauth-protected-resource"},
	}
	for _, tc := range cases {
		if got := resourceMetadataURL(tc.audience); got != tc.want {
			t.Errorf("resourceMetadataURL(%q) = %q, want %q", tc.audience, got, tc.want)
		}
	}
}

func TestExtractScopes(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   []string
	}{
		{"space scope", map[string]any{"scope": "read write admin"}, []string{"read", "write", "admin"}},
		{"scp array", map[string]any{"scp": []any{"read", "write"}}, []string{"read", "write"}},
		{"scp string", map[string]any{"scp": "read write"}, []string{"read", "write"}},
		{"none", map[string]any{}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractScopes(tc.claims)
			if len(got) != len(tc.want) {
				t.Fatalf("extractScopes = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("scope[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
