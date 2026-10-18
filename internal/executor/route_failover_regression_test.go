package executor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestWorkBuddyFifthCandidateCanSucceed(t *testing.T) {
	var seen []string
	e := admittedExecutor(admissionChat{run: func(_ context.Context, id string) (providers.ChatOutcome, error) {
		seen = append(seen, id)
		if len(seen) <= 4 {
			return providers.ChatOutcome{}, &providers.Error{Kind: KindAuth, Status: 401, Scope: "account", Message: "synthetic auth rejection"}
		}
		return providers.ChatOutcome{Content: "fifth healthy", FinishReason: "stop"}, nil
	}})
	for _, id := range []string{"c", "d", "e"} {
		e.Pool.Upsert(Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}})
	}
	result, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if err != nil || result.Content != "fifth healthy" || result.AttemptCount != 5 || len(seen) != 5 {
		t.Fatalf("healthy fifth candidate missed: result=%+v err=%v seen=%v", result, err, seen)
	}
	unique := map[string]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("account repeated: %s", id)
		}
		unique[id] = true
	}
}

func TestWorkBuddyAuthFailureMigratesFailedAffinity(t *testing.T) {
	e := admittedExecutor(admissionChat{run: func(_ context.Context, id string) (providers.ChatOutcome, error) {
		if id == "a" {
			return providers.ChatOutcome{}, &providers.Error{Kind: KindAuth, Status: 401, Scope: "account", Message: "synthetic auth rejection"}
		}
		return providers.ChatOutcome{Content: "healthy", FinishReason: "stop"}, nil
	}})
	e.SessionAffinity.Bind("synthetic-session", "a")
	ctx := WithSessionKey(context.Background(), "synthetic-session")
	result, err := e.ChatNonStream(ctx, translate.ChatRequest{Model: "m"}, "", "workbuddy")
	bound, _ := e.SessionAffinity.Get("synthetic-session")
	if err != nil || result.AccountID != "b" || bound != "b" {
		t.Fatalf("failed affinity retained: result=%+v err=%v bound=%s", result, err, bound)
	}
}

func TestFinalSelectionFailureDoesNotReturnStaleAuth(t *testing.T) {
	e := admittedExecutor(admissionChat{run: func(context.Context, string) (providers.ChatOutcome, error) {
		return providers.ChatOutcome{}, &providers.Error{Kind: KindAuth, Status: 401, Scope: "account", Message: "synthetic auth rejection"}
	}})
	_, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if err == nil || ClassifyError(err).Kind == accounts.KindAuth {
		t.Fatalf("last auth hid final no-candidate result: %v", err)
	}
}

func TestWorkBuddyFirstTwoAuthThirdSucceeds(t *testing.T) {
	hits := 0
	e := admittedExecutor(admissionChat{run: func(context.Context, string) (providers.ChatOutcome, error) {
		hits++
		if hits < 3 {
			return providers.ChatOutcome{}, &providers.Error{Kind: KindAuth, Status: 401, Scope: "account", Message: fmt.Sprintf("synthetic %d", hits)}
		}
		return providers.ChatOutcome{Content: "third", FinishReason: "stop"}, nil
	}})
	e.Pool.Upsert(Item{ID: "c", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}})
	r, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if err != nil || r.AttemptCount != 3 || r.Content != "third" {
		t.Fatalf("result=%+v err=%v hits=%d", r, err, hits)
	}
}

func TestRoutingDiagnosticsExplainUnavailableCandidates(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	f := false
	e.Pool.Upsert(Item{ID: "not-ready", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Ready: &f})
	e.Pool.Upsert(Item{ID: "quota", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}, Quota: &QuotaSnapshot{Exceeded: true}})
	e.Pool.Upsert(Item{ID: "catalog", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"other"}})
	e.Pool.Upsert(Item{ID: "cool", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}, ModelDownUntil: map[string]time.Time{"m": time.Now().Add(time.Hour)}})
	e.Pool.Upsert(Item{ID: "busy", Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}, MaxInFlight: 1, InFlight: 1})
	e.Pool.Upsert(Item{ID: "denied", Provider: "workbuddy", Region: "global", Runtime: "in_process", Models: []string{"m"}})
	var d accounts.RequestRoutingDiagnostic
	e.OnRoute = func(value accounts.RequestRoutingDiagnostic) { d = value }
	_, err := e.ChatNonStream(WithAllowedProviders(context.Background(), []string{"workbuddy:cn"}), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, row := range d.Accounts {
		reasons[row.AccountID] = row.Reason
		if row.AccountID == "a" && row.QuotaKnown {
			t.Fatal("unknown quota treated as known zero")
		}
	}
	for id, want := range map[string]string{"not-ready": "not_ready", "quota": "quota_exhausted", "catalog": "model_catalog", "cool": "model_cooldown", "busy": "account_concurrency", "denied": "provider_permission"} {
		if reasons[id] != want {
			t.Fatalf("%s reason=%s want=%s", id, reasons[id], want)
		}
	}
	// Temporary concurrency saturation remains part of the initial eligible
	// set, even though only two accounts can be acquired at this instant.
	if d.InitialCandidates != 3 || d.Attempted != 1 {
		t.Fatalf("counts=%+v", d)
	}
}

func TestFailedAffinityRemovalCannotEraseNewerBinding(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	e.SessionAffinity.Bind("s", "a")
	_, version, _ := e.SessionAffinity.GetVersion("s")
	e.SessionAffinity.Bind("s", "b")
	if e.SessionAffinity.ForgetVersion("s", "a", version) {
		t.Fatal("late rejection removed newer binding")
	}
	bound, _ := e.SessionAffinity.Get("s")
	if bound != "b" {
		t.Fatal("new binding lost")
	}
}

func TestWorkBuddyAttemptBudgetRespectsConfigAndCancellation(t *testing.T) {
	for _, cancelEarly := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelEarly), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hits := 0
			e := admittedExecutor(admissionChat{run: func(context.Context, string) (providers.ChatOutcome, error) {
				hits++
				if cancelEarly {
					cancel()
					return providers.ChatOutcome{}, context.Canceled
				}
				return providers.ChatOutcome{}, &providers.Error{Kind: KindAuth, Status: 401, Scope: "account", Message: "synthetic auth"}
			}})
			for _, id := range []string{"c", "d", "e"} {
				e.Pool.Upsert(Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}})
			}
			e.MaxAttempts = 2
			var d accounts.RequestRoutingDiagnostic
			e.OnRoute = func(value accounts.RequestRoutingDiagnostic) { d = value }
			_, err := e.ChatNonStream(ctx, translate.ChatRequest{Model: "m"}, "", "workbuddy")
			want := 2
			if cancelEarly {
				want = 1
			}
			if err == nil || hits != want || d.AttemptBudget != 2 {
				t.Fatalf("err=%v hits=%d diagnostic=%+v", err, hits, d)
			}
			if !cancelEarly && d.StopReason != "attempt_limit" {
				t.Fatalf("stop=%s", d.StopReason)
			}
		})
	}
}
