package logs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/store"
)

func TestRoutingDiagnosticSurvivesStreamingAndFinishWrites(t *testing.T) {
	db, err := store.OpenStore(filepath.Join(t.TempDir(), "routing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := NewRequestRecorder(db)
	r.Start(accounts.RequestLog{ID: "req_route", Status: accounts.RequestStatusStarted})
	r.RoutingDiagnostic(accounts.RequestRoutingDiagnostic{RequestID: "req_route", InitialCandidates: 5, AttemptBudget: 5, Attempted: 5, StopReason: "completed"})
	r.Finish(accounts.RequestLog{ID: "req_route", Status: accounts.RequestStatusStreaming})
	r.Finish(accounts.RequestLog{ID: "req_route", Status: accounts.RequestStatusOK})
	r.Close()
	got, err := db.GetRequestLog(context.Background(), "req_route")
	if err != nil {
		t.Fatal(err)
	}
	if got.RoutingDiagnostic == nil || got.RoutingDiagnostic.InitialCandidates != 5 || got.Status != accounts.RequestStatusOK {
		t.Fatalf("routing facts overwritten: %+v", got)
	}
	if err := db.InsertRequestLog(context.Background(), accounts.RequestLog{ID: "req_old"}); err != nil {
		t.Fatal(err)
	}
	old, err := db.GetRequestLog(context.Background(), "req_old")
	if err != nil || old.RoutingDiagnostic != nil {
		t.Fatal("historical route facts invented")
	}
}
