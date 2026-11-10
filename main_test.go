// Full local verification uses `make verify` so the generated-CLI smoke path runs too.
package main_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

// echoRequestCount counts requests that actually reached the upstream /echo
// handler. The MCP e2e test asserts it does not advance when a tool call fails
// server-side input validation (no HTTP call must be made).
var echoRequestCount int64

func TestMain(m *testing.M) {
	// Set up a test server that implements the API described in
	// `./example-cli/openapi.yaml` and start it before running the tests.
	server := &http.Server{Addr: ":8005", Handler: http.DefaultServeMux}

	http.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		atomic.AddInt64(&echoRequestCount, 1)

		body, err := ioutil.ReadAll(r.Body)
		if err != nil {
			panic(err)
		}

		ct := r.Header.Get("Content-Type")
		if ct != "" {
			w.Header().Add("Content-Type", ct)
		}

		// For the test, add the param values to the echoed response.
		var decoded map[string]interface{}
		json.Unmarshal(body, &decoded)
		if decoded == nil {
			decoded = map[string]interface{}{}
		}

		q := r.URL.Query().Get("q")
		if q != "" {
			decoded["q"] = q
		}

		rid := r.Header.Get("X-Request-ID")
		if rid != "" {
			decoded["request-id"] = rid
		}

		// Reflect the inbound Authorization header so the MCP e2e test can
		// assert that outbound auth propagated from the CLI's env var. Only set
		// when present, so the existing no-auth TestEchoSuccess stays green.
		if auth := r.Header.Get("Authorization"); auth != "" {
			decoded["authorization"] = auth
		}

		marshalled, _ := json.Marshal(decoded)
		w.Write(marshalled)
	})

	// A read-only GET operation for the MCP e2e test's annotation assertion.
	http.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"pong":true}`))
	})

	// An operation that always fails upstream, for the MCP e2e test's
	// upstream-error mapping assertion (422 with a JSON body).
	http.HandleFunc("/error", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"message":"unprocessable"}`))
	})

	go func() {
		server.ListenAndServe()
	}()
	defer server.Shutdown(context.Background())

	os.Exit(m.Run())
}

func TestEchoSuccess(t *testing.T) {
	cliPath := filepath.Join(os.TempDir(), "bartolo-example-cli")
	build := exec.Command("go", "build", "-o", cliPath, "./example-cli")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build example-cli: %v\n%s", err, string(out))
	}

	// Call the compiled executable CLI to hit our test server. No -o: this is
	// also the end-to-end check that a piped run serializes rather than tabulates.
	out, err := exec.Command(cliPath, "echo", "hello:", "world", "--echo-query=foo", "--x-request-id", "bar").CombinedOutput()
	if err != nil {
		fmt.Println(string(out))
		panic(err)
	}

	assert.JSONEq(t, "{\"hello\": \"world\", \"q\": \"foo\", \"request-id\": \"bar\"}", string(out))
}

func TestEchoExamplePrintsAndExitsZero(t *testing.T) {
	cliPath := filepath.Join(os.TempDir(), "bartolo-example-cli")
	build := exec.Command("go", "build", "-o", cliPath, "./example-cli")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build example-cli: %v\n%s", err, string(out))
	}

	out, err := exec.Command(cliPath, "echo", "--example").CombinedOutput()
	if err != nil {
		t.Fatalf("echo --example: %v\n%s", err, string(out))
	}

	// The example body is printed verbatim — not the echo server's response,
	// which would include q/request-id keys — proving no request was sent.
	assert.JSONEq(t, "{\"hello\": \"world\"}", string(out))

	// The printed body round-trips through --from-file.
	bodyFile := filepath.Join(t.TempDir(), "body.json")
	if err := os.WriteFile(bodyFile, out, 0600); err != nil {
		t.Fatalf("write body file: %v", err)
	}

	out, err = exec.Command(cliPath, "echo", "--from-file", bodyFile).CombinedOutput()
	if err != nil {
		t.Fatalf("echo --from-file: %v\n%s", err, string(out))
	}
	assert.JSONEq(t, "{\"hello\": \"world\"}", string(out))
}
