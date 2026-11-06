package workbuddy

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestCheckinDoesNotTreatMissingCodeAsSuccessOrExposeUpstreamMessage(t *testing.T) {
	for _, body := range []string{`{"msg":"secret-marker"}`, `{"code":null,"msg":"secret-marker"}`, `{"code":9001,"msg":"secret-marker"}`} {
		t.Run(body, func(t *testing.T) {
			client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			payload, _ := (Credential{AccessToken: "synthetic", UID: "u", Domain: DomainCN, ExpiresAt: 4102444800}).Encode()
			_ = store.SaveCredentialPayload(context.Background(), "acc1", CredentialFormat, payload)
			result, err := client.Checkin(context.Background(), "acc1")
			if err == nil || result.Status != "error" || strings.Contains(result.Message, "secret-marker") || strings.Contains(err.Error(), "secret-marker") {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
func TestDailyCheckinDoesNotRetry9074(t *testing.T) {
	if retryDailyCheckin(context.Background(), nil, 500, []byte(`{"code":9074}`), 0) {
		t.Fatal("9074 retried")
	}
}

func TestCheckinAlreadyRequiresStructuredUpstreamEvidence(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
	}{{500, `{"code":10001,"msg":"already checked in"}`}, {200, `{"msg":"already checked in"}`}, {200, `already checked in`}, {200, `{"code":9074,"msg":"already checked in"}`}} {
		if _, ok := alreadyCheckedInMessage(c.status, []byte(c.body)); ok {
			t.Errorf("unconfirmed already status=%d body=%s", c.status, c.body)
		}
	}
}
