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
	t.Run("no profile in force", func(t *testing.T) {
		resetAuthState(t)
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
		assert.Equal(t, "", config["profile"])
		assert.Equal(t, "", config["profile_server"])
		assert.Equal(t, "https://example.com", config["selected_server"])
	})

	// Exercises doctor through the persisted rung, which a revert to
	// viper.GetString("profile") would miss.
	t.Run("persisted profile selection is authoritative", func(t *testing.T) {
		serverFixture(t, "https://profile.example.com")
		// serverFixture uses the flag-bound key; swap it for a persisted
		// selection so this test can tell the two rungs apart.
		viper.Set("profile", "")
		execute("auth profile use acme")

		out := execute("doctor")

		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("decode doctor output: %v\n%s", err, out)
		}
		config := decoded["config"].(map[string]interface{})

		assert.Equal(t, "acme", config["profile"])
		assert.Equal(t, "https://profile.example.com", config["profile_server"])
		assert.Equal(t, "https://profile.example.com", config["selected_server"])
	})
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
	// A value that maps to itself is already canonical.
	valid := map[string]string{
		"https://api.example.com":           "https://api.example.com",
		"http://localhost:8080":             "http://localhost:8080",
		"https://user:pass@api.example.com": "https://user:pass@api.example.com",

		// Scheme guessed for a remote host, with or without port and path.
		"api.example.com":         "https://api.example.com",
		"api.example.com/v2":      "https://api.example.com/v2",
		"api.example.com:8443":    "https://api.example.com:8443",
		"api.example.com:8443/v2": "https://api.example.com:8443/v2",
		"//api.example.com":       "https://api.example.com",

		// A trailing slash is dropped: server+path would otherwise produce `https://host//things`.
		"https://api.example.com/v2/": "https://api.example.com/v2",

		// Scheme and host are lowered to keep server URLs comparable by string equality.
		"HTTPS://API.example.com":    "https://api.example.com",
		"https://API.example.com/V2": "https://api.example.com/V2",
	}
	for in, want := range valid {
		got, _, err := NormalizeServerURL(in)
		assert.NoErrorf(t, err, "NormalizeServerURL(%q)", in)
		assert.Equalf(t, want, got, "NormalizeServerURL(%q)", in)
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
		_, _, err := NormalizeServerURL(in)
		assert.Errorf(t, err, "NormalizeServerURL(%q)", in)
	}
}

// The bool is what callers use to decide whether to warn that they guessed.
func TestNormalizeServerURLReportsAGuessedScheme(t *testing.T) {
	_, added, err := NormalizeServerURL("api.example.com")
	assert.NoError(t, err)
	assert.True(t, added)

	_, added, err = NormalizeServerURL("https://api.example.com")
	assert.NoError(t, err)
	assert.False(t, added)
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

		// Returned as-is: on the request path a transport error naming the bad
		// URL beats an empty one.
		"ftp://api.example.com": "ftp://api.example.com",
	}

	for raw, want := range cases {
		viper.Reset()
		Init(&Config{AppName: "test"})
		viper.Set("server-default", raw)

		assert.Equalf(t, want, ResolveServer(), "ResolveServer() with server-default %q", raw)
	}
}
