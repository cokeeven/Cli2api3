package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

type attemptMetadataKey struct{}
type attemptMetadata struct{ Provider, Model, Reason, ID string }

func withAttemptMetadata(ctx context.Context, item Item, model, reason, id string) context.Context {
	return context.WithValue(ctx, attemptMetadataKey{}, attemptMetadata{item.Provider, model, reason, id})
}

func rejectedUsageState(c Classified) string {
	if c.RetrySafe {
		return "non_billable"
	}
	return "unknown"
}

func (e ChatExecutor) executionContext(ctx context.Context) (context.Context, context.CancelFunc) {
	duration := e.RequestTimeout
	if duration <= 0 {
		duration = 5 * time.Minute
	}
	return context.WithTimeoutCause(providers.WithChatTimeouts(ctx, e.ChatTimeouts), duration, &providers.ChatTimeoutError{Phase: "total"})
}
func (l *routeLoop) routeReason(e ChatExecutor) string {
	if l.routing.Source == routingStickyEscape {
		if l.routing.Migrate {
			return "quota_migration"
		}
		return "temporary_capacity_borrow"
	}
	if l.routing.Source == routingSticky {
		return "sticky"
	}
	if len(l.identity.Policy.Platforms) > 0 {
		return "priority"
	}
	return "default"
}

func estimateReservation(req translate.ChatRequest) int64 {
	var limit int64
	raw := req.MaxCompletionTokens
	if len(raw) == 0 {
		raw = req.MaxTokens
	}
	if json.Unmarshal(raw, &limit) != nil || limit <= 0 {
		return -1
	}
	encoded, _ := json.Marshal(req)
	var payload any
	_ = json.Unmarshal(encoded, &payload)
	if hasUnboundedMedia(payload) {
		return -1
	}
	if limit > math.MaxInt64-int64(len(encoded)) {
		return -1
	}
	return int64(len(encoded)) + limit
}

func hasUnboundedMedia(value any) bool {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			if key == "image_url" || key == "file_id" || key == "input_audio" || key == "file_data" {
				return true
			}
			if key == "type" {
				if kind, ok := child.(string); ok && (strings.Contains(kind, "image") || strings.Contains(kind, "audio") || strings.Contains(kind, "file") || strings.Contains(kind, "video")) {
					return true
				}
			}
			if hasUnboundedMedia(child) {
				return true
			}
		}
	case []any:
		for _, child := range node {
			if hasUnboundedMedia(child) {
				return true
			}
		}
	}
	return false
}

// usageGuardMessage turns a raw guard reason into an operator-facing sentence.
// The unknown-cost and unknown-token reasons are not plain "limit exceeded"
// errors: they mean the platform cannot supply a trusted bound for this
// request, and the guard refuses it rather than treating the cost as zero.
func usageGuardMessage(reason, provider, model string) string {
	switch reason {
	case "credit_upper_bound_unknown":
		return fmt.Sprintf("usage guard: the %s platform does not report a trusted per-request credit cost for %s, so the configured credit guard (daily_credits/reserve_credits) refuses this paid request instead of assuming zero cost; disable that guard for this account or rely on a token guard", provider, model)
	case "daily_tokens", "model_daily_tokens":
		return fmt.Sprintf("usage guard: %s cannot bound the token count for this %s request (no usable max_tokens or an unbounded attachment), so the token guard refuses it; set max_tokens or shorten the request", provider, model)
	default:
		return "usage guard: " + reason
	}
}

func (l *routeLoop) reserve(e ChatExecutor, item Item) error {
	if e.Budgets == nil {
		return nil
	}
	id := accounts.NewAttemptID()
	reservation := accounts.UsageReservation{ID: id, RequestID: l.requestID, KeyID: l.identity.KeyID, AccountID: item.ID, Model: routeModel(l.request.Model), EstimatedTokens: estimateReservation(l.request), At: time.Now().UTC()}
	reservation.FreeModel = itemModelFree(item, l.request.Model)
	// Trae can exceed a client's max_tokens (including reasoning). Its
	// declared model ceiling is the only available conservative bound.
	if itemProvider(item) == "trae" {
		maxOutput := item.ModelOutputLimits[routeModel(l.request.Model)]
		reservation.EstimatedTokens = -1
		if maxOutput > 0 {
			bounded := l.request
			bounded.MaxCompletionTokens = []byte(strconv.Itoa(maxOutput))
			reservation.EstimatedTokens = estimateReservation(bounded)
		}
	}
	if l.native {
		reservation.EstimatedTokens = -1
	}
	// CreditUpperBound stays nil on purpose. No supported platform exposes a
	// trusted absolute per-request credit cost: the catalog "credits" values
	// are multipliers ("x1.4") or price labels, and a balance difference is a
	// measurement of the past, not a bound on the next request. Inventing a
	// bound from either would let a credit guard admit a request it cannot
	// price, so the store denies it as credit_upper_bound_unknown instead.
	if err := e.Budgets.ReserveUsage(l.ctx, reservation); err != nil {
		classified := Classified{Kind: accounts.KindUnavailable, Status: 503, Code: "usage_budget_unavailable", Type: "api_error", Message: "usage budget storage is unavailable"}
		var guard *accounts.UsageGuardError
		if errors.As(err, &guard) {
			classified.Kind = accounts.KindRateLimit
			classified.Status = 429
			classified.Message = usageGuardMessage(guard.Reason, itemProvider(item), routeModel(l.request.Model))
			classified.Code = "usage_guard_" + guard.Reason
			if !guard.ResetAt.IsZero() {
				classified.RetryAfter = time.Until(guard.ResetAt)
			}
		}
		return NewExecutionError(classified, err)
	}
	l.reservation = id
	if err := e.Budgets.MarkUsageDispatched(l.ctx, id); err != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(l.ctx), 5*time.Second)
		defer cancel()
		_ = e.Budgets.SettleUsage(ctx, accounts.UsageSettlement{ID: id, NonBillable: true, At: time.Now().UTC()})
		l.reservation = ""
		return NewExecutionError(Classified{Kind: accounts.KindUnavailable, Status: 503, Code: "usage_budget_unavailable", Message: "usage budget dispatch could not be recorded"}, err)
	}
	return nil
}

func (l *routeLoop) finishAttempt(e ChatExecutor, result *ChatResult, completion *StreamCompletion, completed bool) {
	if l.release == nil {
		return
	}
	defer l.release()
	l.release = nil
	if e.Budgets == nil || l.reservation == "" {
		return
	}
	settlement := accounts.UsageSettlement{ID: l.reservation, At: time.Now().UTC()}
	l.reservation = ""
	if result != nil {
		if result.UsageSource == "upstream" || result.UsageSource == "provider" {
			prompt, completion := result.UsageTokenEvidence()
			if prompt != nil && completion != nil {
				tokens := int64(*prompt + *completion)
				settlement.ActualTokens = &tokens
			}
		}
		settlement.ActualCredits = result.ConsumedCredits
		if settlement.ActualCredits == nil {
			settlement.ActualCredits = result.Credits
		}
		settlement.UsageSource = result.UsageSource
	}
	if completion != nil {
		if completion.PromptTokens != nil && completion.CompletionTokens != nil && (completion.UsageSource == "upstream" || completion.UsageSource == "provider") {
			tokens := int64(*completion.PromptTokens + *completion.CompletionTokens)
			settlement.ActualTokens = &tokens
		}
		settlement.ActualCredits = completion.Credits
		settlement.UsageSource = completion.UsageSource
	}
	if !completed && completion == nil && l.lastErr != nil {
		classified := ClassifyError(l.lastErr)
		settlement.NonBillable = classified.RetrySafe
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(l.ctx), 5*time.Second)
	defer cancel()
	if err := e.Budgets.SettleUsage(ctx, settlement); err != nil {
		log.Printf("usage settlement pending reservation=%s error=%v", settlement.ID, err)
	}
}

type finalizedBody struct {
	io.ReadCloser
	ctx           context.Context
	fallback      func()
	release       func()
	once          sync.Once
	transportOnce sync.Once
	closeErr      error
}

func (b *finalizedBody) Read(data []byte) (int, error) {
	n, err := b.ReadCloser.Read(data)
	if b.ctx != nil {
		err = providers.ChatContextError(b.ctx, err)
	}
	if err != nil {
		b.release()
	}
	return n, err
}

func (b *finalizedBody) closeTransport() error {
	b.transportOnce.Do(func() { b.closeErr = b.ReadCloser.Close() })
	return b.closeErr
}
func (b *finalizedBody) Close() error { err := b.closeTransport(); b.once.Do(b.fallback); return err }

func (b *finalizedBody) ChatStreamObservation() providers.ChatStreamObservation {
	if observer, ok := b.ReadCloser.(providers.ChatStreamObserver); ok {
		return observer.ChatStreamObservation()
	}
	return providers.ChatStreamObservation{}
}

func (b *finalizedBody) SoloStreamFinishReason() (string, string, string, string) {
	metadata, ok := b.ReadCloser.(interface {
		SoloStreamFinishReason() (string, string, string, string)
	})
	if !ok {
		return "", "", "", ""
	}
	return metadata.SoloStreamFinishReason()
}

func (e ChatExecutor) finalizeStream(ctx context.Context, req translate.ChatRequest, result *StreamResult, loop *routeLoop, item Item, index int) {
	lease := *loop
	loop.release = nil
	loop.reservation = ""
	loop.cancel = nil
	if lease.release != nil {
		release := lease.release
		var releaseOnce sync.Once
		lease.release = func() { releaseOnce.Do(release) }
	}
	var once sync.Once
	var body *finalizedBody
	var stopCancel atomic.Pointer[func() bool]
	result.Finalize = func(completion StreamCompletion) {
		once.Do(func() {
			if lease.cancel != nil {
				defer lease.cancel()
			}
			defer func() {
				lease.finishAttempt(e, nil, &completion, completion.Completed && completion.Err == nil)
				stop := "stream_completed"
				if completion.Err != nil || !completion.Completed {
					stop = "stream_failed"
				}
				lease.finishRouting(e, completion.Err, stop)
			}()
			if stop := stopCancel.Load(); stop != nil {
				(*stop)()
			}
			if body != nil {
				_ = body.closeTransport()
			}
			finished := time.Now().UTC()
			latency := int(finished.Sub(lease.started).Milliseconds())
			attempt := accounts.RequestAttempt{AttemptIndex: index, AccountID: item.ID, StartedAt: lease.started, FinishedAt: &finished, LatencyMs: &latency, Status: accounts.AttemptStatusError, PromptTokens: completion.PromptTokens, CompletionTokens: completion.CompletionTokens, UsageSource: completion.UsageSource}
			attempt.Credits = completion.Credits
			if completion.Completed && completion.Err == nil {
				attempt.Status = accounts.AttemptStatusOK
				attempt.HTTPStatus = ptrInt(200)
				e.markOK(item.ID, req.Model, item.StateVersion)
				e.bindSession(lease.routing, item.ID)
			} else {
				if completion.Err == nil {
					completion.Err = context.Canceled
				}
				lease.lastErr = completion.Err
				classified := ClassifyError(completion.Err)
				e.observeFailedRoute(&lease, item, classified)
				if requestContextDone(ctx, completion.Err) {
					attempt.Status = accounts.AttemptStatusCanceled
				}
				attempt.ErrorScope = classified.Scope
				attempt.ErrorKind = classified.Kind
				attempt.ErrorMessage = truncateErr(completion.Err.Error())
				if !requestContextDone(ctx, completion.Err) {
					e.ObserveStreamFailure(item.ID, completion.Err, req.Model)
				}
			}
			e.recordAttempt(ctx, attempt, completion.Err)
		})
	}
	if result.Response != nil && result.Response.Body != nil {
		release := lease.release
		if release == nil {
			release = func() {}
		}
		body = &finalizedBody{ReadCloser: result.Response.Body, ctx: ctx, release: release, fallback: func() { result.Finalize(StreamCompletion{}) }}
		result.Response.Body = body
		// Cancellation must unblock the upstream read without consuming the
		// one-shot fallback finalizer. The gateway still needs to relay any
		// usage snapshot already read before it records the canceled result.
		stop := context.AfterFunc(ctx, func() {
			_ = body.closeTransport()
			body.release()
		})
		stopCancel.Store(&stop)
	} else {
		result.Finalize(StreamCompletion{Err: StreamIncompleteError()})
	}
}

func (l *routeLoop) preferExpiry(e ChatExecutor, q RouteQuery) string {
	policies, ok := e.Budgets.(accounts.UsagePolicyStore)
	if !ok || q.PreferAccount != "" || e.Pool == nil {
		return q.PreferAccount
	}
	var best Item
	var expiry int64
	now := time.Now()
	for _, item := range e.Pool.Items() {
		if !routeMatches(item, q) || itemDown(item, now) || itemModelDown(item, q, now) || itemSaturated(item) || item.Quota == nil {
			continue
		}
		policy, err := policies.GetUsagePolicy(l.ctx, item.ID)
		if err != nil || !policy.PreferExpiring {
			continue
		}
		for _, pack := range item.Quota.Packages {
			if pack.Remain <= 0 || pack.EndsAt <= now.Unix() || pack.ExpirySource == "cycle" || pack.Status == "expired" || pack.Status == "inactive" || pack.Status == "exhausted" {
				continue
			}
			if expiry == 0 || pack.EndsAt < expiry || (pack.EndsAt == expiry && item.ID < best.ID) {
				best = item
				expiry = pack.EndsAt
			}
		}
	}
	return best.ID
}
