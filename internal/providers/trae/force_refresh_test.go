package trae

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

func TestForceRefreshDespiteFutureExpiry(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(48 * time.Hour).Unix(), RefreshExpiresAt: time.Now().Add(72 * time.Hour).Unix(), UID: "synthetic"})
	var calls atomic.Int32
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"Result":{"Token":"synthetic-new","RefreshToken":"synthetic-next","TokenExpireDuration":3600}}`))
	}))
	defer closeServer()
	if err := client.ForceCredentialRefresh(context.Background(), "acc"); err != nil || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestForceRefreshReportsRejectedExchange(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"Code":"unauthorized","Message":"synthetic rejection"}`))
	}))
	defer closeServer()
	if err := client.ForceCredentialRefresh(context.Background(), "acc"); err == nil {
		t.Fatal("rejected exchange reported success")
	}
}

func TestForceRefreshRejectsLaterAuthAndMissingToken(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	started, release := make(chan struct{}), make(chan struct{})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"Result":{"Token":"synthetic-new","RefreshToken":"synthetic-next","TokenExpireDuration":3600}}`))
	}))
	defer closeServer()
	done := make(chan error, 1)
	go func() { done <- client.ForceCredentialRefresh(context.Background(), "acc") }()
	<-started
	if err := store.Observe(context.Background(), "acc", "", "login_required", "newer failure", accounts.KindAuth); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, providers.ErrCredentialSuperseded) {
		t.Fatalf("conflict error=%v", err)
	}
	missing := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", UID: "synthetic"})
	if err := NewClient(missing).ForceCredentialRefresh(context.Background(), "acc"); !errors.Is(err, providers.ErrCredentialMissing) {
		t.Fatalf("missing error=%v", err)
	}
	missing.payload = []byte(`invalid synthetic payload`)
	if err := NewClient(missing).ForceCredentialRefresh(context.Background(), "acc"); !errors.Is(err, providers.ErrCredentialMissing) {
		t.Fatalf("invalid payload error=%v", err)
	}
}

func TestForceRefreshWaitingForCredentialLockHonorsCancellation(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic", RefreshToken: "synthetic-refresh", UID: "synthetic"})
	client := NewClient(store)
	lock := client.credentialLock("acc")
	lock.Lock()
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := client.ForceCredentialRefresh(ctx, "acc"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error=%v", err)
	}
}
