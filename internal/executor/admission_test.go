package executor

import (
	"context"
	"errors"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAtomicAdmissionSharesAccountAndKeyPlatformAcrossModels(t *testing.T) {
	p := NewPool(nil, nil)
	for _, id := range []string{"a", "b"} {
		p.Upsert(Item{ID: id, Provider: "workbuddy", Region: "cn", MaxInFlight: 2, Models: []string{"m", "n"}})
	}
	identity := routingIdentity{KeyID: "key-1", Policy: accounts.KeyRoutingPolicy{Platforms: []accounts.PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn", MaxInFlight: 3}}}}
	var wg sync.WaitGroup
	var admitted atomic.Int32
	var releaseMu sync.Mutex
	var releases []func()
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := "m"
			if i%2 == 0 {
				model = "n"
			}
			_, release, ok := p.acquireRoute(RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: model}, identity)
			if ok {
				admitted.Add(1)
				releaseMu.Lock()
				releases = append(releases, release)
				releaseMu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if got := admitted.Load(); got != 3 {
		t.Fatalf("admitted=%d, want key cap 3", got)
	}
	for _, item := range p.Items() {
		if item.LiveInFlight > 2 {
			t.Fatalf("account exceeded: %+v", item)
		}
	}
	other := routingIdentity{KeyID: "key-2"}
	_, release, ok := p.acquireRoute(RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "m"}, other)
	if !ok {
		t.Fatal("remaining shared slot unavailable")
	}
	releases = append(releases, release)
	if _, _, ok = p.acquireRoute(RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PublicModel: "n"}, other); ok {
		t.Fatal("other key bypassed shared account caps")
	}
	for _, release := range releases {
		release()
		release()
	}
	if len(p.PlatformInFlight()) != 0 {
		t.Fatal("key platform slots leaked")
	}
	for _, item := range p.Items() {
		if item.LiveInFlight != 0 {
			t.Fatal("account slots leaked")
		}
	}
}

func TestAdmissionSurvivesProbeAndRemoveReadd(t *testing.T) {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", MaxInFlight: 1})
	q := RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn"}
	_, release, ok := p.acquireRoute(q, routingIdentity{})
	if !ok {
		t.Fatal("not admitted")
	}
	p.MergeHealth("a", true, true, 0, 0, "")
	if _, _, ok = p.acquireRoute(q, routingIdentity{}); ok {
		t.Fatal("probe reset live counter")
	}
	p.Remove("a")
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", MaxInFlight: 1})
	if _, _, ok = p.acquireRoute(q, routingIdentity{}); ok {
		t.Fatal("remove/readd reset active lease")
	}
	release()
	_, release, ok = p.acquireRoute(q, routingIdentity{})
	if !ok {
		t.Fatal("release did not free readded account")
	}
	release()
}

type admissionChat struct {
	run func(context.Context, string) (providers.ChatOutcome, error)
}

func (c admissionChat) ChatNonStream(ctx context.Context, id string, _ translate.ChatRequest) (providers.ChatOutcome, error) {
	if c.run != nil {
		return c.run(ctx, id)
	}
	return providers.ChatOutcome{Content: "ok", UsageSource: "upstream", PromptTokens: 2, CompletionTokens: 1}, nil
}
func (c admissionChat) ChatStream(_ context.Context, _ string, _ translate.ChatRequest) (*http.Response, providers.ResolvedChat, error) {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: [DONE]\n\n"))}, providers.ResolvedChat{}, nil
}
func admittedExecutor(chat admissionChat) ChatExecutor {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Runtime: "in_process", MaxInFlight: 1, Models: []string{"m", "n"}})
	p.Upsert(Item{ID: "b", Provider: "workbuddy", Region: "cn", Runtime: "in_process", MaxInFlight: 1, Models: []string{"m", "n"}})
	e := NewChatExecutor(p, "")
	e.Providers = providers.NewRegistry()
	e.Providers.Register(providers.Adapter{ID: "workbuddy", Chat: chat})
	return e
}

func TestStreamAdmissionTerminalAccountingAndDoubleClose(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	var attempts []accounts.RequestAttempt
	e.OnAttempt = func(a accounts.RequestAttempt) { attempts = append(attempts, a) }
	ctx := WithRequestID(WithSessionKey(context.Background(), "session"), "request")
	req := translate.ChatRequest{Model: "m", Stream: true}
	result, err := e.ChatStreamProxy(ctx, req, "", "workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	item, _ := e.Pool.ByID(result.AccountID)
	if item.LiveInFlight != 1 || len(attempts) != 0 {
		t.Fatalf("headers prematurely finalized: %+v attempts=%d", item, len(attempts))
	}
	if _, err = io.ReadAll(result.Response.Body); err != nil {
		t.Fatal(err)
	}
	item, _ = e.Pool.ByID(result.AccountID)
	if item.LiveInFlight != 0 {
		t.Fatal("EOF did not release capacity")
	}
	if len(attempts) != 0 {
		t.Fatal("EOF without protocol completion marked success")
	}
	result.Finalize(StreamCompletion{Completed: true, PromptTokens: ptrInt(2), CompletionTokens: ptrInt(1), UsageSource: "upstream"})
	result.Finalize(StreamCompletion{Err: errors.New("late error")})
	result.Response.Body.Close()
	result.Response.Body.Close()
	if len(attempts) != 1 || attempts[0].Status != accounts.AttemptStatusOK {
		t.Fatalf("attempts=%+v", attempts)
	}
	if binding, ok := e.SessionAffinity.Get("session"); !ok || binding != result.AccountID {
		t.Fatal("terminal success did not bind")
	}
}

func TestStreamCancellationReleasesWithoutSuccess(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	ctx, cancel := context.WithCancel(context.Background())
	result, err := e.ChatStreamProxy(ctx, translate.ChatRequest{Model: "m", Stream: true}, "", "workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		item, _ := e.Pool.ByID(result.AccountID)
		if item.LiveInFlight == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel leaked lease")
		}
		time.Sleep(time.Millisecond)
	}
	item, _ := e.Pool.ByID(result.AccountID)
	if !item.DownUntil.IsZero() {
		t.Fatal("client cancellation cooled healthy account")
	}
}

func TestPlatformAnchorPriorityRandomAndCrossFailover(t *testing.T) {
	p := NewPool(nil, nil)
	for i := 0; i < 10; i++ {
		p.Upsert(Item{ID: "w" + itoa(i), Provider: "workbuddy", Region: "cn", Models: []string{"m"}})
	}
	p.Upsert(Item{ID: "t", Provider: "trae", Region: "cn", Models: []string{"m"}})
	counts := map[string]int{}
	for i := 0; i < 4000; i++ {
		order := p.platformOrder(RouteQuery{PublicModel: "m"}, routingIdentity{})
		counts[order[0]]++
	}
	if counts["trae:cn"] < 1600 || counts["trae:cn"] > 2400 {
		t.Fatalf("random weighted by account count: %+v", counts)
	}
	identity := routingIdentity{KeyID: "key", Policy: accounts.KeyRoutingPolicy{Platforms: []accounts.PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn", Priority: 10}, {Provider: "trae", Region: "cn", Priority: 1}}}}
	p.MarkDown("w0", time.Hour, "cooling")
	for i := 1; i < 10; i++ {
		p.MarkDown("w"+itoa(i), time.Hour, "cooling")
	}
	e := NewChatExecutor(p, "")
	ctx := WithRoutingPolicy(context.Background(), identity.KeyID, identity.Policy)
	loop := e.newRouteLoop(ctx, "", "", translate.ChatRequest{Model: "m"})
	if _, _, err := loop.pickNext(e, "m"); err == nil {
		t.Fatal("blocked preferred platform bypassed cross-provider OFF")
	}
	identity.Policy.CrossProviderFailover = true
	ctx = WithRoutingPolicy(context.Background(), identity.KeyID, identity.Policy)
	loop = e.newRouteLoop(ctx, "", "", translate.ChatRequest{Model: "m"})
	item, _, err := loop.pickNext(e, "m")
	if err != nil || item.Provider != "trae" {
		t.Fatalf("enabled fallback failed: %+v %v", item, err)
	}
	loop.finishAttempt(e, nil, nil, false)
	loop = e.newRouteLoop(ctx, "", "workbuddy", translate.ChatRequest{Model: "m"})
	if _, _, err := loop.pickNext(e, "m"); err == nil {
		t.Fatal("explicit prefix escaped")
	}
}

func TestModelQuotaFailoverPreservesOtherModelAndMigratesSession(t *testing.T) {
	e := admittedExecutor(admissionChat{run: func(_ context.Context, id string) (providers.ChatOutcome, error) {
		if id == "a" {
			return providers.ChatOutcome{}, &providers.Error{Kind: accounts.KindQuota, Scope: "model", RetrySafe: true, RetryAfter: time.Minute, Status: 429, Message: "model quota exhausted"}
		}
		return providers.ChatOutcome{Content: "ok", UsageSource: "upstream"}, nil
	}})
	e.SessionAffinity.Bind("s", "a")
	result, err := e.ChatNonStream(WithSessionKey(context.Background(), "s"), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if err != nil || result.AccountID != "b" || result.AttemptCount != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if bound, _ := e.SessionAffinity.Get("s"); bound != "b" {
		t.Fatal("confirmed quota did not migrate binding")
	}
	item, _ := e.Pool.ByID("a")
	if itemModelDown(item, RouteQuery{PublicModel: "n"}, time.Now()) || item.Quota != nil && item.Quota.Exceeded {
		t.Fatal("model quota blocked other model")
	}
}

func TestSessionCASAndCapacityBorrow(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	e.SessionAffinity.Bind("s", "a")
	_, version, _ := e.SessionAffinity.GetVersion("s")
	_, release, ok := e.Pool.acquireRoute(RouteQuery{ProviderFilter: "workbuddy", RegionFilter: "cn", PreferAccount: "a"}, routingIdentity{})
	if !ok {
		t.Fatal("not admitted")
	}
	defer release()
	result, err := e.ChatNonStream(WithSessionKey(context.Background(), "s"), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if err != nil || result.AccountID != "b" {
		t.Fatalf("borrow=%+v err=%v", result, err)
	}
	if bound, _ := e.SessionAffinity.Get("s"); bound != "a" {
		t.Fatal("temporary capacity borrow migrated binding")
	}
	e.SessionAffinity.BindVersion("s", "b", version)
	e.SessionAffinity.BindVersion("s", "a", version)
	if bound, _ := e.SessionAffinity.Get("s"); bound != "b" {
		t.Fatal("late success overwrote newer migration")
	}
	e.SessionAffinity.BindVersion("new", "a", 0)
	e.SessionAffinity.BindVersion("new", "b", 0)
	if bound, _ := e.SessionAffinity.Get("new"); bound != "a" {
		t.Fatal("parallel first successes overwrote first binding")
	}
}

func TestBalanceBlockAllowsProvenFreeAndRefresh(t *testing.T) {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "a", Provider: "workbuddy", Region: "cn", Models: []string{"paid", "free"}, FreeModels: []string{"free"}})
	p.MarkClassified("a", Classified{Kind: KindQuota, Scope: "account", RetrySafe: true})
	if got, ok := p.PickRoute(RouteQuery{PublicModel: "paid"}); !ok || got.Quota == nil || !got.Quota.Exceeded {
		t.Fatal("paid route did not surface blocked hint")
	}
	if _, ok := p.PickRoute(RouteQuery{PublicModel: "free"}); !ok {
		t.Fatal("proven free model blocked by exhausted paid credits")
	}
	p.MergeQuota("a", &QuotaSnapshot{Remaining: 100})
	item, ok := p.PickRoute(RouteQuery{PublicModel: "paid"})
	if !ok || item.Quota.Exceeded {
		t.Fatal("confirmed quota refresh did not recover")
	}
}

type admissionBudget struct {
	reserveErr error
	reserves   []accounts.UsageReservation
	settled    []accounts.UsageSettlement
}

func (b *admissionBudget) ReserveUsage(_ context.Context, r accounts.UsageReservation) error {
	b.reserves = append(b.reserves, r)
	return b.reserveErr
}
func (b *admissionBudget) MarkUsageDispatched(context.Context, string) error { return nil }
func (b *admissionBudget) SettleUsage(_ context.Context, s accounts.UsageSettlement) error {
	b.settled = append(b.settled, s)
	return nil
}
func (b *admissionBudget) RecoverUsageReservations(context.Context) error { return nil }

func TestBudgetStorageFailureStopsBeforeDispatchAndReleases(t *testing.T) {
	var dispatched int
	e := admittedExecutor(admissionChat{run: func(context.Context, string) (providers.ChatOutcome, error) {
		dispatched++
		return providers.ChatOutcome{}, nil
	}})
	budget := &admissionBudget{reserveErr: errors.New("database unavailable")}
	e.Budgets = budget
	_, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m", MaxTokens: []byte("5")}, "", "workbuddy")
	if err == nil || ClassifyError(err).Status != 503 || len(budget.reserves) != 1 || dispatched != 0 {
		t.Fatalf("err=%v reserves=%d dispatched=%d", err, len(budget.reserves), dispatched)
	}
	for _, item := range e.Pool.Items() {
		if item.LiveInFlight != 0 {
			t.Fatal("storage failure leaked lease")
		}
	}
}

func TestBudgetGuardSkipsDoNotConsumeUpstreamAttempts(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	budget := &admissionBudget{reserveErr: &accounts.UsageGuardError{Reason: "daily_tokens"}}
	e.Budgets = budget
	result, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m", MaxTokens: []byte("5")}, "", "workbuddy")
	if err == nil || ClassifyError(err).Status != 429 || ClassifyError(err).Code != "usage_guard_daily_tokens" || result.AttemptCount != 0 || len(budget.reserves) != 2 {
		t.Fatalf("result=%+v err=%v reserves=%d", result, err, len(budget.reserves))
	}
}

func TestAttemptAndBudgetIDsMatchAndSettleRealUsage(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	budget := &admissionBudget{}
	e.Budgets = budget
	var attempt accounts.RequestAttempt
	e.OnAttempt = func(a accounts.RequestAttempt) { attempt = a }
	_, err := e.ChatNonStream(WithRequestID(context.Background(), "request"), translate.ChatRequest{Model: "m", MaxTokens: []byte("5")}, "", "workbuddy")
	if err != nil || len(budget.reserves) != 1 || len(budget.settled) != 1 {
		t.Fatalf("err=%v reserves=%d settled=%d", err, len(budget.reserves), len(budget.settled))
	}
	if attempt.ID != budget.reserves[0].ID || budget.settled[0].ID != attempt.ID {
		t.Fatal("attempt and reservation cannot be correlated")
	}
	if budget.settled[0].ActualTokens == nil || *budget.settled[0].ActualTokens != 3 {
		t.Fatalf("settlement=%+v", budget.settled[0])
	}
}

func TestRetriesAreCappedAtFourAndNeverRepeatAnAccount(t *testing.T) {
	var seen []string
	e := admittedExecutor(admissionChat{run: func(_ context.Context, id string) (providers.ChatOutcome, error) {
		seen = append(seen, id)
		return providers.ChatOutcome{}, &providers.Error{Kind: KindRateLimit, Status: 429, Message: "limited"}
	}})
	for _, id := range []string{"c", "d", "e", "f"} {
		e.Pool.Upsert(Item{ID: id, Provider: "workbuddy", Region: "cn", Runtime: "in_process", Models: []string{"m"}})
	}
	e.MaxAttempts = 99
	result, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if err == nil || result.AttemptCount != 4 || len(seen) != 4 {
		t.Fatalf("result=%+v err=%v seen=%v", result, err, seen)
	}
	unique := map[string]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Fatal("same account attempted twice")
		}
		unique[id] = true
	}
	for _, item := range e.Pool.Items() {
		if item.LiveInFlight != 0 {
			t.Fatal("retry leaked lease")
		}
	}
}

func TestPlatformScopedFailureDoesNotHammerOtherAccounts(t *testing.T) {
	var hits int
	e := admittedExecutor(admissionChat{run: func(context.Context, string) (providers.ChatOutcome, error) {
		hits++
		return providers.ChatOutcome{}, &providers.Error{Kind: KindRateLimit, Scope: "platform", RetryAfter: time.Minute, Status: 429, Message: "platform throttled"}
	}})
	_, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if err == nil || hits != 1 {
		t.Fatalf("err=%v hits=%d", err, hits)
	}
}

func TestTokenReservationRejectsUnboundedInputs(t *testing.T) {
	for _, req := range []translate.ChatRequest{
		{Model: "m"},
		{Model: "m", MaxTokens: []byte("9223372036854775807")},
		{Model: "m", MaxTokens: []byte("100"), Messages: []translate.ChatMessage{{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://image.invalid/image.png"}}}}}},
	} {
		if estimateReservation(req) != -1 {
			t.Fatalf("unbounded request admitted: %+v", req)
		}
	}
}

func TestLateSuccessfulStreamCannotClearNewerQuota(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	result, err := e.ChatStreamProxy(context.Background(), translate.ChatRequest{Model: "m", Stream: true}, "a", "workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	e.markClassified("a", Classified{Kind: KindQuota, Scope: "model", RetrySafe: true, Cooldown: time.Hour}, "m")
	before, _ := e.Pool.ByID("a")
	result.Finalize(StreamCompletion{Completed: true})
	result.Response.Body.Close()
	after, _ := e.Pool.ByID("a")
	if after.ModelDownUntil["m"] != before.ModelDownUntil["m"] {
		t.Fatal("older stream cleared newly observed quota")
	}
}

func TestCompatibleResponsesRespectsPlatformBeforeNativePreference(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	e.Pool.Upsert(Item{ID: "q", Provider: "qoder", Region: "global", Runtime: "in_process", Models: []string{"m"}})
	e.Providers.Register(providers.Adapter{ID: "qoder", Chat: admissionChat{}, NativeResponses: &nativeOnlyStreamer{}})
	policy := accounts.KeyRoutingPolicy{Platforms: []accounts.PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn", Priority: 10}, {Provider: "qoder", Region: "global", Priority: 1}}}
	ctx := WithRoutingPolicy(context.Background(), "key", policy)
	req := translate.ChatRequest{Model: "m"}
	selected, native := e.SelectResponsesRoute(ctx, req, "", "", false)
	if native {
		t.Fatal("compatible request escaped preferred WorkBuddy just because Qoder supports native")
	}
	loop := e.newRouteLoop(selected, "", "", req)
	item, _, err := loop.pickNext(e, "m")
	if err != nil || item.Provider != "workbuddy" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	loop.finishAttempt(e, nil, nil, false)
	selected, native = e.SelectResponsesRoute(ctx, req, "", "", true)
	if !native {
		t.Fatal("native-only request did not filter protocol capability before initial anchor")
	}
	loop = e.newRouteLoop(selected, "", "", req)
	loop.eligible = func(item Item) bool { return itemProvider(item) == "qoder" }
	item, _, err = loop.pickNext(e, "m")
	if err != nil || item.Provider != "qoder" {
		t.Fatalf("native item=%+v err=%v", item, err)
	}
	loop.finishAttempt(e, nil, nil, false)
}

func TestResponsesRandomAnchorIsSelectedOnce(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	e.Pool.Upsert(Item{ID: "q", Provider: "qoder", Region: "global", Runtime: "in_process", Models: []string{"m"}})
	e.Providers.Register(providers.Adapter{ID: "qoder", Chat: admissionChat{}, NativeResponses: &nativeOnlyStreamer{}})
	for i := 0; i < 100; i++ {
		ctx, native := e.SelectResponsesRoute(context.Background(), translate.ChatRequest{Model: "m"}, "", "", false)
		loop := e.newRouteLoop(ctx, "", "", translate.ChatRequest{Model: "m"})
		item, _, err := loop.pickNext(e, "m")
		if err != nil {
			t.Fatal(err)
		}
		if native != (item.Provider == "qoder") {
			t.Fatalf("random anchor changed between selection and execution: native=%v provider=%s", native, item.Provider)
		}
		loop.finishAttempt(e, nil, nil, false)
	}
}

func TestTraeReservationUsesDeclaredOutputCeiling(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	e.Pool = NewPool(nil, nil)
	e.Pool.Upsert(Item{ID: "t", Provider: "trae", Region: "cn", Runtime: "in_process", Models: []string{"m"}})
	e.Providers.Register(providers.Adapter{ID: "trae", Chat: admissionChat{}})
	budget := &admissionBudget{}
	e.Budgets = budget
	req := translate.ChatRequest{Model: "m", MaxTokens: []byte("64")}
	if _, err := e.ChatNonStream(context.Background(), req, "", "trae"); err != nil {
		t.Fatal(err)
	}
	if budget.reserves[0].EstimatedTokens != -1 {
		t.Fatal("client max_tokens was assumed to cap Trae reasoning")
	}
	e.Pool.MergeOutputLimits("t", map[string]int{"m": 32000})
	if _, err := e.ChatNonStream(context.Background(), req, "", "trae"); err != nil {
		t.Fatal(err)
	}
	if budget.reserves[1].EstimatedTokens < 32000 {
		t.Fatalf("reservation=%+v", budget.reserves[1])
	}
}

func TestUnifiedRequestDeadlineReleasesAttempts(t *testing.T) {
	e := admittedExecutor(admissionChat{run: func(ctx context.Context, _ string) (providers.ChatOutcome, error) {
		<-ctx.Done()
		return providers.ChatOutcome{}, ctx.Err()
	}})
	e.RequestTimeout = 20 * time.Millisecond
	started := time.Now()
	result, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "m"}, "", "workbuddy")
	if !errors.Is(err, context.DeadlineExceeded) || result.AttemptCount != 1 || time.Since(started) > time.Second {
		t.Fatalf("result=%+v err=%v duration=%v", result, err, time.Since(started))
	}
	for _, item := range e.Pool.Items() {
		if item.LiveInFlight != 0 {
			t.Fatal("timeout leaked slot")
		}
	}
}

type transportCloseObserver struct {
	closed atomic.Bool
	reader io.Reader
}

func (b *transportCloseObserver) Read(data []byte) (int, error) { return b.reader.Read(data) }
func (b *transportCloseObserver) Close() error                  { b.closed.Store(true); return nil }

func TestFinalizeClosesTransportBeforeReleasingAttempt(t *testing.T) {
	e := admittedExecutor(admissionChat{})
	body := &transportCloseObserver{reader: strings.NewReader("data: error\n\n")}
	ctx := WithRequestID(context.Background(), "request")
	loop := e.newRouteLoop(ctx, "", "workbuddy", translate.ChatRequest{Model: "m"})
	item, index, err := loop.pickNext(e, "m")
	if err != nil {
		t.Fatal(err)
	}
	result := StreamResult{Response: &http.Response{Body: body, StatusCode: 200}}
	e.OnAttempt = func(accounts.RequestAttempt) {
		if !body.closed.Load() {
			t.Error("attempt finalized before raw upstream body closed")
		}
	}
	e.finalizeStream(ctx, translate.ChatRequest{Model: "m"}, &result, &loop, item, index)
	result.Finalize(StreamCompletion{Err: errors.New("upstream failure")})
	if !body.closed.Load() {
		t.Fatal("transport remained open")
	}
	result.Response.Body.Close()
}

func TestEveryPlatformPriorityTierRotatesFairly(t *testing.T) {
	p := NewPool(nil, nil)
	for _, provider := range []string{"workbuddy", "trae", "qoder"} {
		p.Upsert(Item{ID: provider, Provider: provider, Region: "cn", Models: []string{"m"}})
	}
	identity := routingIdentity{KeyID: "key", Policy: accounts.KeyRoutingPolicy{Platforms: []accounts.PlatformRoutePolicy{{Provider: "workbuddy", Region: "cn", Priority: 10}, {Provider: "trae", Region: "cn", Priority: 1}, {Provider: "qoder", Region: "cn", Priority: 1}}}}
	first := p.platformOrder(RouteQuery{PublicModel: "m"}, identity)
	second := p.platformOrder(RouteQuery{PublicModel: "m"}, identity)
	if first[0] != "workbuddy:cn" || second[0] != "workbuddy:cn" || first[1] == second[1] {
		t.Fatalf("tiers unfair: %v then %v", first, second)
	}
}

func TestQuotaSnapshotPreservesExecutorErrorAfterUpstreamMerge(t *testing.T) {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "q", Provider: "qoder", Region: "global", Models: []string{"paid", "free"}, FreeModels: []string{"free"}, Quota: &QuotaSnapshot{Exceeded: true}})
	if _, ok := p.PickRoute(RouteQuery{PublicModel: "paid"}); ok {
		t.Fatal("paid account should be filtered by the upstream pool contract")
	}
	e := NewChatExecutor(p, "")
	_, err := e.ChatNonStream(context.Background(), translate.ChatRequest{Model: "paid"}, "", "qoder")
	if err == nil || ClassifyError(err).Kind != KindQuota {
		t.Fatalf("snapshot exhaustion lost its quota cause: %v", err)
	}
}

func TestNativeMappingSnapshotDoesNotAliasLivePool(t *testing.T) {
	p := NewPool(nil, nil)
	p.Upsert(Item{ID: "q", Provider: "qoder"})
	p.MergeNativeModels("q", map[string]string{"public": "native"})
	items := p.Items()
	items[0].NativeModels["public"] = "changed"
	current, _ := p.ByID("q")
	if NativeModelID(current, "public") != "native" {
		t.Fatal("native map snapshot aliases live account")
	}
}
