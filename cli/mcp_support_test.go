package cli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	gentleman "gopkg.in/h2non/gentleman.v2"
)

func TestMarkPassedParams(t *testing.T) {
	params := viper.New()
	MarkPassedParams(params, "archived")
	MarkPassedParams(params, "limit")

	if !FlagPassed(params, "archived") || !FlagPassed(params, "limit") {
		t.Errorf("both names should be marked as passed")
	}
	if FlagPassed(params, "other") {
		t.Errorf("an unmarked name must not read as passed")
	}
	if _, ok := RequestParams(params)[passedFlagsKey]; ok {
		t.Errorf("RequestParams must hide the bookkeeping key")
	}
}

// Cancelling the context bound with SetRequestContext aborts the in-flight
// request instead of waiting for the upstream to answer.
func TestSetRequestContextCancelsRequest(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	params := viper.New()
	SetRequestContext(params, ctx)
	if _, ok := RequestParams(params)[requestContextKey]; ok {
		t.Errorf("RequestParams must hide the request context")
	}

	req := gentleman.New().URL(srv.URL).Request()
	HandleBefore("test", params, req)

	time.AfterFunc(50*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		_, err := req.Do()
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request was not aborted by the cancelled context")
	}
}

func TestResponseErrorIsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"bad name"}`))
	}))
	defer srv.Close()

	resp, err := gentleman.New().URL(srv.URL).Request().Do()
	if err != nil {
		t.Fatal(err)
	}
	var httpErr *HTTPError
	if !errors.As(ResponseError(resp), &httpErr) {
		t.Fatalf("ResponseError should return *HTTPError so callers can read the status")
	}
	if httpErr.StatusCode != http.StatusUnprocessableEntity || string(httpErr.Body) != `{"error":"bad name"}` {
		t.Errorf("HTTPError = %d %q", httpErr.StatusCode, httpErr.Body)
	}
}

// syncBuffer is a bytes.Buffer safe to write from the abandoned read's
// goroutine while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A read abandoned after the grace period finishes whenever the pipe closes,
// long after the caller moved on. It must not log then: the line would report a
// body nobody used, written concurrently with whatever logs next.
func TestAbandonedStdinReadDoesNotLog(t *testing.T) {
	logged := &syncBuffer{}
	previous := zlog.Logger
	zlog.Logger = zerolog.New(logged).Level(zerolog.DebugLevel)
	t.Cleanup(func() { zlog.Logger = previous })

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if _, ok, err := readStdinWithin(reader, 10*time.Millisecond); ok || err != nil {
		t.Fatalf("idle pipe: ok=%v err=%v, want the grace period to expire", ok, err)
	}
	writer.Close() // the parked read now returns EOF

	time.Sleep(100 * time.Millisecond)
	if got := logged.String(); got != "" {
		t.Errorf("the abandoned read logged after the caller moved on: %s", got)
	}
}

// failingAuthHandler fails any request it is asked to authenticate, so a test
// can prove a request never reached it.
type failingAuthHandler struct{}

func (failingAuthHandler) ProfileKeys() []string { return []string{"api-key"} }
func (failingAuthHandler) OnRequest(_ *zerolog.Logger, _ *http.Request) error {
	return errors.New("the configured credentials were consulted")
}

// A request made on a caller's behalf sends that caller's credential and never
// consults this process's own: an anonymous caller must not borrow it, and a
// caller must never act as another.
func TestRequestCredentialReplacesConfiguredAuth(t *testing.T) {
	resetAuthState(t)
	Init(&Config{AppName: "test-auth", EnvPrefix: "TEST_AUTH"})
	UseAuth("", failingAuthHandler{})

	seen := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	params := viper.New()
	SetRequestContext(params, WithRequestCredential(context.Background(), "Bearer sk-caller"))
	req := Client.URL(srv.URL).Get()
	HandleBefore("test", params, req)

	if _, err := req.Do(); err != nil {
		t.Fatalf("request: %v", err)
	}
	if got := <-seen; got != "Bearer sk-caller" {
		t.Errorf("Authorization = %q, want the caller's credential", got)
	}

	// Without one, the configured handler authenticates as before.
	if _, err := Client.URL(srv.URL).Get().Do(); err == nil {
		t.Errorf("a request without a caller credential should go through the configured handler")
	}
}
