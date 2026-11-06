package workbuddy

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

func TestForceRefreshDespiteFutureExpiryAndRejection(t *testing.T) {
	for _, status := range []int{200, 401} {
		store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
		var calls atomic.Int32
		client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
			if status == 200 {
				_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"synthetic-new","refreshToken":"synthetic-next","expiresIn":3600}}`))
			} else {
				_, _ = w.Write([]byte(`{"code":12153,"msg":"synthetic rejection"}`))
			}
		}))
		err := client.ForceCredentialRefresh(context.Background(), "acc")
		closeServer()
		if calls.Load() != 1 || (status == 200 && err != nil) || (status == 401 && err == nil) {
			t.Fatalf("status=%d calls=%d err=%v", status, calls.Load(), err)
		}
	}
}

func TestForceRefreshRejectsLaterAuthAndMissingToken(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	started, release := make(chan struct{}), make(chan struct{})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"synthetic-new","refreshToken":"synthetic-next","expiresIn":3600}}`))
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
	missingClient := NewClient(missing)
	if err := missingClient.ForceCredentialRefresh(context.Background(), "acc"); !errors.Is(err, providers.ErrCredentialMissing) {
		t.Fatalf("missing error=%v", err)
	}
	missing.payload = []byte(`invalid synthetic payload`)
	if err := missingClient.ForceCredentialRefresh(context.Background(), "acc"); !errors.Is(err, providers.ErrCredentialMissing) {
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
