package trae

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestReworkTraeSuccessfulKeepaliveRefreshRecordsRecovery(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	store.status, store.kind = "login_required", accounts.KindAuth
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Result":{"Token":"synthetic-new","RefreshToken":"synthetic-next","TokenExpireDuration":3600}}`))
	}))
	defer closeServer()
	if err := client.Keepalive(context.Background(), "acc"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	status, kind := store.status, store.kind
	store.mu.Unlock()
	if status != "ready" || kind != "" {
		t.Fatalf("successful Trae refresh left auth state latched: status=%q kind=%q", status, kind)
	}
}

func TestReworkTraeFailedKeepaliveRefreshDoesNotRecoverAuthState(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	store.status, store.kind = "login_required", accounts.KindAuth
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"Code":"temporary"}`, http.StatusServiceUnavailable)
	}))
	defer closeServer()
	if err := client.Keepalive(context.Background(), "acc"); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if store.status != "login_required" || store.kind != accounts.KindAuth {
		t.Fatalf("failed Trae refresh changed auth state: status=%q kind=%q", store.status, store.kind)
	}
}

func TestReworkTraeRefreshCannotOverwriteLaterAuthFailure(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	store.status, store.kind = "login_required", accounts.KindAuth
	started, release := make(chan struct{}), make(chan struct{})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"Result":{"Token":"synthetic-stale-refresh","RefreshToken":"synthetic-stale-next","TokenExpireDuration":3600}}`))
	}))
	defer closeServer()
	finished := make(chan error, 1)
	go func() { finished <- client.Keepalive(context.Background(), "acc") }()
	<-started
	if err := store.Observe(context.Background(), "acc", "synthetic", "login_required", "newer synthetic auth failure", accounts.KindAuth); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	_, payload, err := store.LoadCredentialPayload(context.Background(), "acc")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := DecodeCredential(payload)
	if err != nil || credential.AccessToken != "synthetic-old" {
		t.Fatalf("stale Trae refresh overwrote credential: %+v err=%v", credential, err)
	}
	store.mu.Lock()
	status, lastError, kind := store.status, store.account.LastError, store.kind
	store.mu.Unlock()
	if status != "login_required" || kind != accounts.KindAuth || lastError != "newer synthetic auth failure" {
		t.Fatalf("later Trae auth failure overwritten: status=%q kind=%q error=%q", status, kind, lastError)
	}
}
