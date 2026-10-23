package workbuddy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestReviewerTraceIdentifierDoesNotChangeAuthClassification(t *testing.T) {
	plain := Classify(200, `{"code":11133,"msg":"invalid parameters"}`)
	traced := Classify(200, `{"code":11133,"msg":"invalid parameters","requestId":"trace-12153-in-identifier"}`)
	t.Logf("plain kind=%s code=%s; traced kind=%s code=%s", plain.Kind, plain.Code, traced.Kind, traced.Code)
	if plain.Kind != traced.Kind {
		t.Fatalf("diagnostic requestId changed classification from %s to %s", plain.Kind, traced.Kind)
	}
	success := Classify(200, `{"code":0,"msg":"ok","requestId":"trace-12153-in-identifier"}`)
	if success.Kind != "" {
		t.Fatalf("successful envelope classified as an account error because of requestId: %+v", success)
	}
	dead := Classify(200, `{"code":12153,"msg":"Offline user session not found","requestId":"trace-plain"}`)
	if dead.Kind != accounts.KindAuth || dead.Code != "12153" {
		t.Fatalf("structured session-dead code was not preserved: %+v", dead)
	}
}

func TestReviewerWorkBuddyAuthEvidenceSeparatesBusinessAndHTTPStatus(t *testing.T) {
	httpStatus := http.StatusOK
	err := classifiedErrorWithEvidence(http.StatusOK, &httpStatus,
		[]byte(`{"code":12153,"msg":"Offline user session not found","requestId":"synthetic-trace"}`),
		"chat", "workbuddy_chat_response")
	var providerErr *providers.Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error type=%T, want *providers.Error", err)
	}
	if providerErr.Kind != accounts.KindAuth || providerErr.Code != "12153" || providerErr.Status != http.StatusUnauthorized {
		t.Fatalf("normalized classification lost: %+v", providerErr)
	}
	if providerErr.HTTPStatus == nil || *providerErr.HTTPStatus != http.StatusOK {
		t.Fatalf("observed HTTP status=%v, want 200", providerErr.HTTPStatus)
	}
	if providerErr.Phase != "chat" || providerErr.Source != "workbuddy_chat_response" {
		t.Fatalf("phase/source=%q/%q", providerErr.Phase, providerErr.Source)
	}
	if !strings.Contains(providerErr.Message, "会话") || strings.Contains(providerErr.Message, "synthetic-trace") {
		t.Fatalf("message is not localized or contains request data: %q", providerErr.Message)
	}

	missing := workBuddyCredentialReadError(accounts.ErrSecretNotFound, "workbuddy_credential_store")
	if !errors.As(missing, &providerErr) || providerErr.Phase != "credential_read" || providerErr.HTTPStatus != nil {
		t.Fatalf("missing credential evidence=%+v", providerErr)
	}
}

func TestReviewerChatBusinessErrorKeepsHTTP200ThroughSSEAggregation(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path != pathChat {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("data: {\"error\":{\"code\":12153,\"message\":\"Offline user session not found\",\"requestId\":\"synthetic-12153\"}}\n\n"))
	}))
	payload, err := (Credential{AccessToken: "synthetic-access", UID: "synthetic-uid", Domain: "codebuddy.cn", ExpiresAt: 4102444800}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	store.items = map[string][]byte{"synthetic-account": payload}
	_, err = client.ChatNonStream(context.Background(), "synthetic-account", translate.ChatRequest{
		Model: "synthetic-model", Messages: []translate.ChatMessage{{Role: "user", Content: "synthetic prompt"}},
	})
	var providerErr *providers.Error
	if !errors.As(err, &providerErr) {
		t.Fatalf("error type=%T, err=%v", err, err)
	}
	if providerErr.Kind != accounts.KindAuth || providerErr.Code != "12153" || providerErr.HTTPStatus == nil || *providerErr.HTTPStatus != http.StatusOK {
		t.Fatalf("business/HTTP evidence=%+v", providerErr)
	}
	if providerErr.Phase != "chat" || providerErr.Source != "workbuddy_sse_aggregate" {
		t.Fatalf("phase/source=%q/%q", providerErr.Phase, providerErr.Source)
	}
	if strings.Contains(providerErr.Message, "synthetic-12153") || strings.Contains(providerErr.Message, "synthetic prompt") {
		t.Fatalf("private request data leaked: %q", providerErr.Message)
	}
}
