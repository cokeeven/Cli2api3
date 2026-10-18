package logs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/store"
)

func TestRecorderKeepsStartMessageShapeWhenFinishOmitsIt(t *testing.T) {
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "request-shape.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recorder := NewRequestRecorder(db)
	now := time.Now().UTC()
	recorder.Start(accounts.RequestLog{
		ID: "req_async_shape", CreatedAt: now, Status: accounts.RequestStatusStarted,
		RequestedModel: "trae/test", MessageCount: 3, MessageShapeKnown: true,
		EmptyMessageIndexes: []int{1}, MessageRoles: []string{"system", "user", "assistant"},
		SourceFingerprint: "src_synthetic",
	})
	recorder.Finish(accounts.RequestLog{
		ID: "req_async_shape", CreatedAt: now, FinishedAt: timePtr(now.Add(time.Second)),
		Status: accounts.RequestStatusOK, RequestedModel: "trae/test", AttemptCount: 1,
	})
	recorder.Close()

	got, err := db.GetRequestLog(context.Background(), "req_async_shape")
	if err != nil {
		t.Fatal(err)
	}
	if got.SourceFingerprint != "src_synthetic" || got.Status != accounts.RequestStatusOK || !got.MessageShapeKnown || got.MessageCount != 3 ||
		len(got.EmptyMessageIndexes) != 1 || got.EmptyMessageIndexes[0] != 1 ||
		len(got.MessageRoles) != 3 || got.MessageRoles[0] != "system" || got.MessageRoles[2] != "assistant" {
		t.Fatalf("asynchronous finish overwrote the start shape: %+v", got)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
