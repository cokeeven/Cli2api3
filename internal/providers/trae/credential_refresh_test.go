package trae

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
	s.account.CredentialGeneration++
	return nil
}

func (s *credentialTestStore) SaveCredentialPayloadIfCurrent(_ context.Context, _ string, expectedFormat string, expectedPayload []byte, format string, payload []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.format != expectedFormat || string(s.payload) != string(expectedPayload) {
		return false, nil
	}
	s.format = format
	s.payload = append([]byte(nil), payload...)
	s.account.CredentialGeneration++
	return true, nil
}

func (s *credentialTestStore) SaveCredentialPayloadAfterRefreshIfCurrent(_ context.Context, _ string, expectedFormat string, expectedPayload []byte, format string, payload []byte, expectedCredentialGeneration int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.account.CredentialGeneration != expectedCredentialGeneration || s.format != expectedFormat || string(s.payload) != string(expectedPayload) {
		return false, nil
	}
	s.format = format
	s.payload = append([]byte(nil), payload...)
	if s.status != "quota_exhausted" && (s.status == "login_required" || s.status == "auth_failed" || s.kind == accounts.KindAuth) {
		s.status, s.kind = "ready", ""
		s.account.Status, s.account.LastError, s.account.LastErrorKind = "ready", "", ""
	}
	s.account.CredentialGeneration++
	return true, nil
}

func (s *credentialTestStore) Observe(_ context.Context, _, _, status, lastError, kind string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.kind = status, kind
	s.account.Status, s.account.LastError, s.account.LastErrorKind = status, lastError, kind
	if status == "login_required" || status == "auth_failed" || kind == accounts.KindAuth {
		s.account.CredentialGeneration++
	}
	return nil
}

func newCredentialTestStore(t *testing.T, credential Credential) *credentialTestStore {
	t.Helper()
	payload, err := credential.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return &credentialTestStore{
		account: accounts.Account{ID: "acc", Provider: "trae", ProviderRegion: "cn", UpdatedAt: time.Now().UTC()},
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

func TestCredentialTransientRefreshFailureUsesOnlyStillValidAccessToken(t *testing.T) {
	original := Credential{AccessToken: "valid-access", RefreshToken: "refresh", ExpiresAt: time.Now().Add(time.Minute).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"}
	store := newCredentialTestStore(t, original)
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"Code":"temporary"}`, http.StatusServiceUnavailable)
	}))
	defer closeServer()

	got, err := client.credential(context.Background(), "acc")
	if err != nil || got.AccessToken != original.AccessToken {
		t.Fatalf("valid access fallback token=%q err=%v", got.AccessToken, err)
	}
	if store.status == "login_required" || store.kind == accounts.KindAuth {
		t.Fatalf("temporary refresh failure marked login required: status=%q kind=%q", store.status, store.kind)
	}
}

func TestCredentialExpiredAccessRefreshFailureReturnsErrorWithoutFalseAuth(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "expired", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"Code":"temporary"}`, http.StatusServiceUnavailable)
	}))
	defer closeServer()

	if _, err := client.credential(context.Background(), "acc"); err == nil {
		t.Fatal("expired access token was returned after a temporary refresh failure")
	}
	health, probeErr := client.Probe(context.Background(), "acc")
	if probeErr != nil || health.LastErrorKind != accounts.KindUnavailable {
		t.Fatalf("temporary refresh error classification: health=%+v err=%v", health, probeErr)
	}
	if store.status == "login_required" || store.kind == accounts.KindAuth {
		t.Fatalf("temporary refresh failure marked auth: status=%q kind=%q", store.status, store.kind)
	}
}

func TestCredentialExpiredRefreshTokenUsesStillValidAccessWithoutRefresh(t *testing.T) {
	original := Credential{AccessToken: "valid-access", RefreshToken: "expired-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), RefreshExpiresAt: time.Now().Add(-time.Minute).Unix(), UID: "synthetic"}
	store := newCredentialTestStore(t, original)
	var calls atomic.Int32
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected refresh", http.StatusUnauthorized)
	}))
	defer closeServer()

	got, err := client.credential(context.Background(), "acc")
	if err != nil || got.AccessToken != original.AccessToken || calls.Load() != 0 {
		t.Fatalf("still-valid access after refresh expiry token=%q calls=%d err=%v", got.AccessToken, calls.Load(), err)
	}
}

func TestCredentialConcurrentRefreshIsDeduplicated(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathExchange {
			t.Errorf("path=%s", r.URL.Path)
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{"Token": "fresh", "RefreshToken": "rotated", "TokenExpireDuration": 3600}})
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
	store := newCredentialTestStore(t, Credential{AccessToken: "old", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	started, release := make(chan struct{}), make(chan struct{})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"Result": map[string]any{"Token": "stale-refresh", "RefreshToken": "stale-rotated", "TokenExpireDuration": 3600}})
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
	login := Credential{AccessToken: "login-access", RefreshToken: "login-refresh", ExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"}
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

func TestCredentialRefreshFailureClassifiesRateLimitWithoutAuthWrite(t *testing.T) {
	store := newCredentialTestStore(t, Credential{AccessToken: "expired", RefreshToken: "refresh", ExpiresAt: time.Now().Add(-time.Minute).Unix(), RefreshExpiresAt: time.Now().Add(time.Hour).Unix(), UID: "synthetic"})
	client, closeServer := newCredentialTestClient(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, `{"Code":4011,"Message":"rate limited"}`, http.StatusTooManyRequests)
	}))
	defer closeServer()

	if _, err := client.credential(context.Background(), "acc"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "refresh") {
		t.Fatalf("err=%v", err)
	}
	if store.status == "login_required" || store.kind == accounts.KindAuth {
		t.Fatalf("rate limit marked auth: status=%q kind=%q", store.status, store.kind)
	}
}
