package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
)

func TestDoctorCommand(t *testing.T) {
	Init(&Config{
		AppName:   "test",
		EnvPrefix: "TEST",
		Version:   "1.2.3",
	})

	RegisterServers([]map[string]string{
		{
			"description": "Test server",
			"url":         "https://example.com",
		},
	})

	out := execute("doctor")

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode doctor output: %v\n%s", err, out)
	}

	app := decoded["app"].(map[string]interface{})
	config := decoded["config"].(map[string]interface{})

	assert.Equal(t, "test", app["name"])
	assert.Equal(t, "1.2.3", app["version"])
	assert.Equal(t, "https://example.com", config["selected_server"])
}

func TestRequestCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hello":"world"}`))
	}))
	defer server.Close()

	Init(&Config{
		AppName: "test",
	})

	out := execute("request get " + server.URL + "/hello")

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode request output: %v\n%s", err, out)
	}

	assert.Equal(t, true, decoded["ok"])
	assert.EqualValues(t, 200, decoded["status"])

	body := decoded["body"].(map[string]interface{})
	assert.Equal(t, "world", body["hello"])
}

func TestServerUseCommand(t *testing.T) {
	viper.Reset()
	Init(&Config{
		AppName: "test",
	})

	RegisterServers([]map[string]string{
		{"description": "Prod", "url": "https://prod.example.com"},
		{"description": "Staging", "url": "https://staging.example.com"},
	})

	out := execute("server use 1")

	var decoded map[string]interface{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode server use output: %v\n%s", err, out)
	}

	assert.Equal(t, "https://staging.example.com", decoded["server"])
	assert.EqualValues(t, 1, decoded["index"])
	assert.Equal(t, "https://staging.example.com", ResolveServer())
}

func TestNormalizeServerURL(t *testing.T) {
	unchanged := []string{
		"https://api.example.com",
		"http://localhost:8080",
		"https://user:pass@api.example.com",
	}
	for _, in := range unchanged {
		got, added, err := NormalizeServerURL(in)
		if err != nil || added || got != in {
			t.Errorf("NormalizeServerURL(%q) = (%q, %v, %v), want (%q, false, nil)", in, got, added, err, in)
		}
	}

	rewritten := map[string]string{
		// Scheme guessed for a remote host, with or without port and path.
		"api.example.com":         "https://api.example.com",
		"api.example.com/v2":      "https://api.example.com/v2",
		"api.example.com:8443":    "https://api.example.com:8443",
		"api.example.com:8443/v2": "https://api.example.com:8443/v2",
		"//api.example.com":       "https://api.example.com",
	}
	// A trailing slash is dropped: server+path would otherwise produce `https://host//things`.
	rewritten["https://api.example.com/v2/"] = "https://api.example.com/v2"
	for in, want := range rewritten {
		got, _, err := NormalizeServerURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeServerURL(%q) = (%q, _, %v), want (%q, _, nil)", in, got, err, want)
		}
	}

	// Scheme and host are lowered to keep server URLs comparable by string equality.
	canonicalized := map[string]string{
		"HTTPS://API.example.com":    "https://api.example.com",
		"https://API.example.com/V2": "https://api.example.com/V2",
	}
	for in, want := range canonicalized {
		got, _, err := NormalizeServerURL(in)
		if err != nil || got != want {
			t.Errorf("NormalizeServerURL(%q) = (%q, _, %v), want (%q, _, nil)", in, got, err, want)
		}
	}

	invalid := []string{
		"",
		"  ",
		"ftp://api.example.com",
		"https://",
		"://x",
		// Loopback must name its scheme; http is as likely as https.
		"localhost:8080",
		"localhost",
		"127.0.0.1:8080",
		"dev.localhost:3000",
		// Protocol-relative values reach the same loopback rule as bare hosts.
		"//localhost:8080",
		// A mistyped scheme is not a host: it would otherwise become `https://https:/x`.
		"https:/api.example.com",
		"https:api.example.com",
		// A bare host with a local part would silently become URL credentials.
		"user@api.example.com",
		// server+path concatenation puts a query or fragment mid-URL.
		"https://api.example.com/v2?token=abc",
		"https://api.example.com/v2#frag",
	}
	for _, in := range invalid {
		if got, _, err := NormalizeServerURL(in); err == nil {
			t.Errorf("NormalizeServerURL(%q) = (%q, _, nil), want error", in, got)
		}
	}
}

// ResolveServer normalizes on the read path, not only where a value is written:
// a config or credentials file can be written by an older bartolo, edited by
// hand, or come from <PREFIX>_SERVER_DEFAULT, none of which pass through a
// write-time check.
func TestResolveServerNormalizesPersistedValues(t *testing.T) {
	cases := map[string]string{
		"api.example.com":          "https://api.example.com",
		"HTTPS://API.example.com":  "https://api.example.com",
		"https://api.example.com/": "https://api.example.com",
	}

	for raw, want := range cases {
		viper.Reset()
		Init(&Config{AppName: "test"})
		viper.Set("server-default", raw)

		if got := ResolveServer(); got != want {
			t.Errorf("ResolveServer() with server-default %q = %q, want %q", raw, got, want)
		}
	}

	// Returned as-is: on the request path a transport error naming the bad URL beats an empty one.
	viper.Reset()
	Init(&Config{AppName: "test"})
	viper.Set("server-default", "ftp://api.example.com")

	if got := ResolveServer(); got != "ftp://api.example.com" {
		t.Errorf("ResolveServer() with an unusable default = %q, want it returned unchanged", got)
	}
}
