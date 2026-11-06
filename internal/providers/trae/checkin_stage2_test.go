package trae

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestCheckinEvidenceClassifies9074WithoutLeakingResponse(t *testing.T) {
	count := 0
	c, s := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		_, _ = w.Write([]byte(`{"code":9074,"message":"secret-marker"}`))
	}))
	seedCheckinCredential(t, s)
	result, err := c.Checkin(context.Background(), "acc1")
	if err == nil || count != 1 || !strings.Contains(result.Message, "outcome=upstream_rejected_9074") || !strings.Contains(result.Message, "phase=status") || !strings.Contains(result.Message, "http=200") || !strings.Contains(result.Message, "attempts=1") || strings.Contains(result.Message, "secret-marker") {
		t.Fatalf("result=%+v err=%v count=%d", result, err, count)
	}
}
func TestCheckinEvidenceMissingCredential(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected request") }))
	result, err := c.Checkin(context.Background(), "absent")
	if err == nil || !strings.Contains(result.Message, "outcome=credential_missing") || !strings.Contains(result.Message, "attempts=0") {
		t.Fatalf("%+v %v", result, err)
	}
}
