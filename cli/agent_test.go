package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
