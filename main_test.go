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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	// Set up a test server that implements the API described in
	// `./example-cli/openapi.yaml` and start it before running the tests.
	server := &http.Server{Addr: ":8005", Handler: http.DefaultServeMux}

	http.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

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

		q := r.URL.Query().Get("q")
		if q != "" {
			decoded["q"] = q
		}

		rid := r.Header.Get("X-Request-ID")
		if rid != "" {
			decoded["request-id"] = rid
		}

		marshalled, _ := json.Marshal(decoded)
		w.Write(marshalled)
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

	// Call the compiled executable CLI to hit our test server.
	out, err := exec.Command(cliPath, "echo", "hello:", "world", "--echo-query=foo", "--x-request-id", "bar").CombinedOutput()
	if err != nil {
		fmt.Println(string(out))
		panic(err)
	}

	assert.JSONEq(t, "{\"hello\": \"world\", \"q\": \"foo\", \"request-id\": \"bar\"}", string(out))
}
