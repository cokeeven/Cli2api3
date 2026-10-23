package workbuddy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
)

type reworkGetErrorStore struct {
	*memStore
	getErr error
}

func (s *reworkGetErrorStore) Get(context.Context, string) (accounts.Account, error) {
	return accounts.Account{}, s.getErr
}

func TestReworkMissingStoredCredentialUsesRealStoreSentinel(t *testing.T) {
	client := NewClient(&memStore{items: map[string][]byte{}})
	health, err := client.Probe(context.Background(), "synthetic-existing-account")
	if err != nil {
		t.Fatal(err)
	}
	if health.LastErrorKind != accounts.KindAuth || !strings.Contains(health.LastError, "登录凭据缺失") {
		t.Fatalf("existing account missing credential: health=%+v", health)
	}
}

func TestReworkMissingAccountOrStoreFailureIsNotAuthentication(t *testing.T) {
	for name, getErr := range map[string]error{
		"account_missing": accounts.ErrAccountNotFound,
		"database_error":  errors.New("synthetic database unavailable"),
	} {
		t.Run(name, func(t *testing.T) {
			store := &reworkGetErrorStore{memStore: &memStore{items: map[string][]byte{}}, getErr: getErr}
			client := NewClient(store)
			health, err := client.Probe(context.Background(), "synthetic-missing-account")
			if err != nil {
				t.Fatal(err)
			}
			if health.LastErrorKind == accounts.KindAuth {
				t.Fatalf("nonexistent account or database error became auth: %+v", health)
			}
		})
	}
}

func TestReworkSuccessfulRefreshRecordsRecovery(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: 0, Domain: DomainCN, UID: "synthetic"})
	store.status, store.kind = "login_required", accounts.KindAuth
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"synthetic-new","refreshToken":"synthetic-next","expiresIn":3600}}`))
	}))
	defer closeServer()
	if _, err := client.Refresh(context.Background(), "acc", Credential{}); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	status, kind := store.status, store.kind
	store.mu.Unlock()
	if status != "ready" || kind != "" {
		t.Fatalf("successful refresh left auth state latched: status=%q kind=%q", status, kind)
	}
}

func TestReworkWorkBuddyKeepaliveRefreshRecordsRecovery(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: 0, Domain: DomainCN, UID: "synthetic"})
	store.status, store.kind = "login_required", accounts.KindAuth
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"synthetic-new","refreshToken":"synthetic-next","expiresIn":3600}}`))
	}))
	defer closeServer()
	if err := client.Keepalive(context.Background(), "acc"); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	status, kind := store.status, store.kind
	store.mu.Unlock()
	if status != "ready" || kind != "" {
		t.Fatalf("successful WorkBuddy Keepalive refresh left auth state latched: status=%q kind=%q", status, kind)
	}
}

func TestReworkFailedRefreshDoesNotRecoverAuthState(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: 0, Domain: DomainCN, UID: "synthetic"})
	store.status, store.kind = "login_required", accounts.KindAuth
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"code":10001,"msg":"synthetic failure"}`, http.StatusServiceUnavailable)
	}))
	defer closeServer()
	if _, err := client.Refresh(context.Background(), "acc", Credential{}); err == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if store.status != "login_required" || store.kind != accounts.KindAuth {
		t.Fatalf("failed refresh cleared auth state: status=%q kind=%q", store.status, store.kind)
	}
}

func TestReworkRefreshRecoveryRequiresSuccessfulUpstreamEvidence(t *testing.T) {
	var providerErr *providers.Error
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: 0, Domain: DomainCN, UID: "synthetic"})
	store.status, store.kind = "login_required", accounts.KindAuth
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":12153,"msg":"synthetic session rejected"}`))
	}))
	defer closeServer()
	_, err := client.Refresh(context.Background(), "acc", Credential{})
	if !errors.As(err, &providerErr) || providerErr.Kind != accounts.KindAuth {
		t.Fatalf("refresh error=%v, want auth rejection", err)
	}
	if store.status != "login_required" || store.kind != accounts.KindAuth {
		t.Fatalf("failed auth refresh changed state: status=%q kind=%q", store.status, store.kind)
	}
}

func TestReworkRefreshCannotOverwriteLaterAuthFailure(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: 0, Domain: DomainCN, UID: "synthetic"})
	store.status, store.kind = "login_required", accounts.KindAuth
	started, release := make(chan struct{}), make(chan struct{})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"synthetic-stale-refresh","refreshToken":"synthetic-stale-next","expiresIn":3600}}`))
	}))
	defer closeServer()
	store.mu.Lock()
	initialGeneration := store.account.CredentialGeneration
	store.mu.Unlock()
	type result struct {
		credential Credential
		err        error
	}
	finished := make(chan result, 1)
	go func() {
		credential, err := client.Refresh(context.Background(), "acc", Credential{})
		finished <- result{credential: credential, err: err}
	}()
	<-started
	if err := store.Observe(context.Background(), "acc", "synthetic", "login_required", "newer synthetic auth failure", accounts.KindAuth); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	authFailureGeneration := store.account.CredentialGeneration
	store.mu.Unlock()
	if authFailureGeneration <= initialGeneration {
		t.Fatalf("new auth failure did not advance generation: before=%d after=%d", initialGeneration, authFailureGeneration)
	}
	close(release)
	got := <-finished
	if got.err != nil || got.credential.AccessToken != "synthetic-old" {
		store.mu.Lock()
		expectedGeneration, casCalls := store.refreshCASExpectedGeneration, store.refreshCASCalls
		store.mu.Unlock()
		t.Fatalf("late refresh result=%q err=%v initial_generation=%d auth_failure_generation=%d cas_expected_generation=%d cas_calls=%d", got.credential.AccessToken, got.err, initialGeneration, authFailureGeneration, expectedGeneration, casCalls)
	}
	store.mu.Lock()
	status, kind, accountError := store.status, store.kind, store.account.LastError
	format, payload := store.format, append([]byte(nil), store.payload...)
	store.mu.Unlock()
	if status != "login_required" || kind != accounts.KindAuth || accountError != "newer synthetic auth failure" {
		t.Fatalf("later auth failure overwritten: status=%q kind=%q error=%q", status, kind, accountError)
	}
	credential, err := DecodeCredential(payload)
	if err != nil || credential.AccessToken != "synthetic-old" || format != CredentialFormat {
		t.Fatalf("stale refresh overwrote current credential: %+v err=%v", credential, err)
	}
}
