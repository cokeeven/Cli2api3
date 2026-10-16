package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Drain is the upgrade's "finish in-flight business" hook: it must block while
// a request is being served, unblock when that request completes, and report
// a timeout honestly instead of hanging the upgrade forever.
func TestDrainWaitsForInFlightRequests(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/slow", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	})
	server := &Server{mux: mux, inflight: &requestTracker{}}
	handler := server.Handler()

	go func() {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/slow", nil))
	}()
	<-entered

	if drained := server.Drain(mustTimeout(t, 50*time.Millisecond)); drained {
		t.Fatal("Drain must not report idle while a request is being served")
	}
	if server.ActiveRequests() != 1 {
		t.Fatalf("active = %d, want 1", server.ActiveRequests())
	}
	close(release)
	if drained := server.Drain(mustTimeout(t, 2*time.Second)); !drained {
		t.Fatal("Drain must report idle once the in-flight request finished")
	}
}

// The upgrade's own control endpoints must not be counted as business, or the
// upgrade would wait on itself while it polls its job status.
func TestDrainIgnoresUpdateEndpoints(t *testing.T) {
	server := &Server{mux: http.NewServeMux(), inflight: &requestTracker{}}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/system/update", nil))
	if server.ActiveRequests() != 0 {
		t.Fatalf("update endpoint was counted as business: %d", server.ActiveRequests())
	}
	if drained := server.Drain(mustTimeout(t, 100*time.Millisecond)); !drained {
		t.Fatal("Drain must be immediate when nothing is in flight")
	}
}

func mustTimeout(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
