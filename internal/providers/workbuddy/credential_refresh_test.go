package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

type credentialTestStore struct {
	mu      sync.Mutex
	account accounts.Account
	format  string
	payload []byte
	status  string
	kind    string
}

func (s *credentialTestStore) Get(context.Context, string) (accounts.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.account, nil
}

func (s *credentialTestStore) LoadCredentialPayload(context.Context, string) (string, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.format, append([]byte(nil), s.payload...), nil
}

func (s *credentialTestStore) SaveCredentialPayload(_ context.Context, _ string, format string, payload []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.format = format
	s.payload = append([]byte(nil), payload...)
	return nil
}

func (s *credentialTestStore) SaveCredentialPayloadIfCurrent(_ context.Context, _ string, oldFormat string, oldPayload []byte, newFormat string, newPayload []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.format != oldFormat || !strings.EqualFold(string(s.payload), string(oldPayload)) {
		return false, nil
	}
	s.format = newFormat
	s.payload = append([]byte(nil), newPayload...)
	return true, nil
}

func (s *credentialTestStore) Observe(_ context.Context, _, _, status, _, kind string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.kind = status, kind
	return nil
}

func newCredentialTestStore(t *testing.T, credential Credential) *credentialTestStore {
	t.Helper()
	payload, err := credential.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &credentialTestStore{
		account: accounts.Account{ID: "acc", Provider: "workbuddy", ProviderRegion: "cn"},
		format:  CredentialFormat,
		payload: payload,
	}
}

func newCredentialTestClient(store *credentialTestStore, handler http.Handler) (*Client, func()) {
	server := httptest.NewServer(handler)
	client := NewClient(store)
	client.http = server.Client()
	client.http.Transport = rewriteTransport{server: server.URL, round: server.Client().Transport}
	return client, server.Close
}

func TestCredentialExpiredRefreshFailureIsReturned(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "old-access", RefreshToken: "old-refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), Domain: DomainCN, UID: "uid"})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":10001,"msg":"temporary upstream failure"}`, http.StatusServiceUnavailable)
	}))
	defer closeServer()

	credential, err := client.credential(context.Background(), "acc")
	if err == nil {
		t.Fatalf("expired access token was returned after refresh failure: token=%q", credential.AccessToken)
	}
	health, probeErr := client.Probe(context.Background(), "acc")
	if probeErr != nil || health.LastErrorKind != accounts.KindUnavailable {
		t.Fatalf("temporary refresh error classification: health=%+v err=%v", health, probeErr)
	}
}

func TestCredentialTransientRefreshFailureMayUseStillValidToken(t *testing.T) {
	original := Credential{AccessToken: "valid-access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(45 * time.Second).Unix(), Domain: DomainCN, UID: "uid"}
	store := newCredentialTestStore(t, original)
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":10001,"msg":"temporary upstream failure"}`, http.StatusServiceUnavailable)
	}))
	defer closeServer()

	got, err := client.credential(context.Background(), "acc")
	if err != nil || got.AccessToken != original.AccessToken {
		t.Fatalf("still-valid token should be a bounded fallback: token=%q err=%v", got.AccessToken, err)
	}
}

func TestCredentialConcurrentRefreshIsDeduplicated(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), Domain: DomainCN, UID: "uid"})
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathTokenRefresh {
			t.Errorf("path=%s", r.URL.Path)
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"accessToken": "fresh", "refreshToken": "rotated", "expiresIn": 3600}})
	}))
	defer closeServer()

	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			got, err := client.credential(context.Background(), "acc")
			if err == nil && got.AccessToken != "fresh" {
				err = errors.New("caller did not observe refreshed credential")
			}
			results <- err
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond)
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d, want one", calls.Load())
	}
}

func TestCredentialLateRefreshCannotOverwriteRelogin(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), Domain: DomainCN, UID: "uid"})
	started := make(chan struct{})
	release := make(chan struct{})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"accessToken": "stale-refresh", "refreshToken": "stale-rotated", "expiresIn": 3600}})
	}))
	defer closeServer()

	type outcome struct {
		credential Credential
		err        error
	}
	finished := make(chan outcome, 1)
	go func() {
		credential, err := client.credential(context.Background(), "acc")
		finished <- outcome{credential: credential, err: err}
	}()
	<-started
	login := Credential{AccessToken: "login-access", RefreshToken: "login-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), Domain: DomainCN, UID: "uid"}
	loginPayload, err := login.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCredentialPayload(context.Background(), "acc", CredentialFormat, loginPayload); err != nil {
		t.Fatal(err)
	}
	close(release)

	got := <-finished
	if got.err != nil || got.credential.AccessToken != login.AccessToken {
		t.Fatalf("late refresh result=%q err=%v", got.credential.AccessToken, got.err)
	}
	_, payload, err := store.LoadCredentialPayload(context.Background(), "acc")
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := DecodeCredential(payload)
	if err != nil || persisted.AccessToken != login.AccessToken || persisted.RefreshToken != login.RefreshToken {
		t.Fatalf("re-login credential was overwritten: %+v err=%v", persisted, err)
	}
}

func TestKeepaliveRefreshesOnlyWhenCredentialIsDue(t *testing.T) {
	var calls atomic.Int32
	store := &credentialTestStore{account: accounts.Account{ID: "acc", Provider: "workbuddy", ProviderRegion: "cn"}}
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathTokenRefresh {
			t.Errorf("path=%s", r.URL.Path)
		}
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"accessToken": "renewed", "refreshToken": "rotated", "expiresIn": 3600}})
	}))
	defer closeServer()

	valid := Credential{AccessToken: "still-valid", RefreshToken: "refresh", ExpiresAt: time.Now().Add(48 * time.Hour).Unix(), Domain: DomainCN, UID: "synthetic"}
	validPayload, _ := valid.Encode()
	if err := store.SaveCredentialPayload(context.Background(), "valid", CredentialFormat, validPayload); err != nil {
		t.Fatal(err)
	}
	if err := client.Keepalive(context.Background(), "valid"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("valid credential triggered %d refreshes", calls.Load())
	}

	due := Credential{AccessToken: "near-expiry", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), Domain: DomainCN, UID: "synthetic"}
	duePayload, _ := due.Encode()
	if err := store.SaveCredentialPayload(context.Background(), "due", CredentialFormat, duePayload); err != nil {
		t.Fatal(err)
	}
	if err := client.Keepalive(context.Background(), "due"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("due credential refresh calls=%d", calls.Load())
	}
	_, saved, err := store.LoadCredentialPayload(context.Background(), "due")
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := DecodeCredential(saved)
	if err != nil || refreshed.AccessToken != "renewed" || refreshed.RefreshAfter <= time.Now().Unix() {
		t.Fatalf("refreshed credential=%+v err=%v", refreshed, err)
	}
}
