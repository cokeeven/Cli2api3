package logs

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/store"
)

func TestRecorderOmitsStructuredUpstreamEchoesFromDiagnostics(t *testing.T) {
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "privacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := NewRequestRecorder(db)
	now := time.Now().UTC()
	message := `upstream rejected {"messages":[{"content":"synthetic-private-prompt"}],"tool_calls":[{"arguments":"synthetic-private-tool"}],"Authorization":"Bearer synthetic-private-auth","Cookie":"synthetic-private-cookie"}`
	r.Start(accounts.RequestLog{ID: "req_privacy", CreatedAt: now, Status: accounts.RequestStatusStarted, ErrorMessage: message})
	r.Attempt(accounts.RequestAttempt{ID: "attempt_privacy", RequestID: "req_privacy", StartedAt: now, Status: accounts.AttemptStatusError, ErrorMessage: message})
	r.StreamDiagnostic(accounts.RequestStreamDiagnostic{RequestID: "req_privacy", CreatedAt: now, RelayError: message, ContextErr: message})
	r.Finish(accounts.RequestLog{ID: "req_privacy", CreatedAt: now, Status: accounts.RequestStatusError, ErrorKind: accounts.KindRateLimit, ErrorCode: "3004", ErrorMessage: message})
	r.Close()
	result, err := db.GetRequestLog(context.Background(), "req_privacy")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "synthetic-private") {
		t.Fatal("upstream echo persisted sensitive fields")
	}
	if result.ErrorKind != accounts.KindRateLimit || result.ErrorCode != "3004" {
		t.Fatal("classification evidence lost")
	}
}
