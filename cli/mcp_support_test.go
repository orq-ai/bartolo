package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
