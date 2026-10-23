package runtime

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/providers/qoder"
)

// maxConcurrentModelFetches bounds simultaneous upstream catalog fetches for
// the whole process, shared by every EnsureModelCatalogs call.
const maxConcurrentModelFetches = 4

// Per-account catalog snapshots on Pool items. Qoder uses worker /admin/models;
// in-process providers use adapter.Models(). Does not merge with API display cache.

const (
	modelCatalogTTL = 5 * time.Minute
	// modelCatalogBackoff caps how soon a failed account is retried, so one
	// unreachable upstream cannot be hammered at the callers' pace.
	modelCatalogBackoff = 30 * time.Second
)

// catalogRefreshState tracks the shared, process-wide catalog refresh
// machinery on the Manager. The semaphore and per-account inflight map are
// shared across every EnsureModelCatalogs call, so N concurrent callers
// still bound total upstream catalog traffic to maxConcurrentModelFetches
// and each account is fetched at most once at a time.
type catalogRefreshState struct {
	sem      chan struct{}
	mu       sync.Mutex
	running  map[string]bool
	failures map[string]time.Time
}

// EnsureModelCatalogs refreshes per-account catalogs that are missing or
// older than the worker TTL. Failures leave the previous snapshot in place
// and never flip readiness or cooldown.
//
// Concurrency contract:
//   - the semaphore is shared on the Manager, so concurrent calls never
//     multiply the number of simultaneous upstream fetches;
//   - an account already being refreshed is skipped (per-account dedup);
//   - an account that failed recently is skipped for modelCatalogBackoff
//     instead of being retried on every request.
//
// The caller returns immediately; a nil Models slice means unknown (fail
// open) until the background refresh completes and subsequent requests use
// the fresh catalog.
func (m *Manager) EnsureModelCatalogs(ctx context.Context, force bool) {
	if m == nil || m.pool == nil {
		return
	}
	state := m.catalogRefresh()
	now := time.Now()
	var stale []Item
	for _, item := range m.pool.Items() {
		// A known Trae authentication failure usually means the local account
		// record has no usable credential payload. Repeated catalog refreshes
		// cannot repair that state and only retry a request that is known to
		// fail locally. A successful probe after login moves the item back to
		// ready, so its catalog refresh can resume normally.
		if item.Provider == "trae" && item.RuntimeState == "auth_failed" {
			continue
		}
		if !force && item.Models != nil && !item.ModelsAt.IsZero() && now.Sub(item.ModelsAt) < modelCatalogTTL {
			continue
		}
		stale = append(stale, item)
	}
	if len(stale) == 0 {
		return
	}
	// Fire background refreshes concurrently with the bounded shared
	// semaphore. Use m.runCtx so refreshes survive the caller's request
	// context and are canceled only on shutdown.
	if ctx.Err() != nil {
		return // caller context already cancelled — no point firing goroutines
	}
	for _, item := range stale {
		m.startCatalogRefresh(state, item)
	}
}

// catalogRefresh lazily creates the shared refresh state. Managers built by
// NewManager have it pre-created; zero-value Managers (tests) get it on
// first use.
func (m *Manager) catalogRefresh() *catalogRefreshState {
	m.catalogOnce.Do(func() {
		if m.catalog == nil {
			m.catalog = &catalogRefreshState{
				sem:      make(chan struct{}, maxConcurrentModelFetches),
				running:  map[string]bool{},
				failures: map[string]time.Time{},
			}
		}
	})
	return m.catalog
}

// startCatalogRefresh deduplicates per account and applies failure backoff.
// It never blocks: when the account is already refreshing or in backoff, the
// call returns without scheduling anything.
func (m *Manager) startCatalogRefresh(state *catalogRefreshState, item Item) {
	if item.ID == "" {
		return
	}
	state.mu.Lock()
	if state.running[item.ID] {
		state.mu.Unlock()
		return
	}
	if until, ok := state.failures[item.ID]; ok {
		if time.Now().Before(until) {
			state.mu.Unlock()
			return
		}
		delete(state.failures, item.ID)
	}
	state.running[item.ID] = true
	state.mu.Unlock()

	// Serialize task registration with Close's cancellation and Wait.
	m.mu.Lock()
	if m.runCtx.Err() != nil {
		m.mu.Unlock()
		state.mu.Lock()
		delete(state.running, item.ID)
		state.mu.Unlock()
		return
	}
	m.maintenanceDone.Add(1)
	epoch := m.accountEpoch[item.ID]
	m.mu.Unlock()
	go func(it Item) {
		defer m.maintenanceDone.Done()
		defer func() {
			state.mu.Lock()
			delete(state.running, it.ID)
			state.mu.Unlock()
		}()
		select {
		case state.sem <- struct{}{}:
			defer func() { <-state.sem }()
		case <-m.runCtx.Done():
			return
		}
		if !m.fetchAccountModelsAt(m.runCtx, it, epoch) {
			// Refresh failed upstream; back off so request-paced callers
			// cannot hammer one broken account on every call.
			m.mu.Lock()
			if m.catalogGenerationCurrentLocked(m.runCtx, it, epoch) {
				state.mu.Lock()
				state.failures[it.ID] = time.Now().Add(modelCatalogBackoff)
				state.mu.Unlock()
			}
			m.mu.Unlock()
		}
	}(item)
}

// fetchAccountModels fetches one account's catalog and merges it into the
// pool. It reports whether the upstream fetch succeeded; a nil adapter or
// unsupported provider reports true (nothing failed — there is just nothing
// to fetch), so only real upstream errors engage backoff.
func (m *Manager) fetchAccountModels(ctx context.Context, item Item) bool {
	if m == nil || m.pool == nil || item.ID == "" {
		return true
	}
	m.mu.Lock()
	epoch := m.accountEpoch[item.ID]
	m.mu.Unlock()
	return m.fetchAccountModelsAt(ctx, item, epoch)
}

// A catalog belongs to the lifecycle generation which queued it. An upstream
// may ignore cancellation, so both queue admission and the merge need a check.
func (m *Manager) catalogGenerationCurrentLocked(ctx context.Context, item Item, epoch uint64) bool {
	if ctx.Err() != nil || (m.runCtx != nil && m.runCtx.Err() != nil) || m.accountEpoch[item.ID] != epoch {
		return false
	}
	current, ok := m.pool.ByID(item.ID)
	return ok && current.Provider == item.Provider && current.Region == item.Region && current.URL == item.URL && current.Runtime == item.Runtime
}

func (m *Manager) withCurrentCatalog(ctx context.Context, item Item, epoch uint64, apply func()) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.catalogGenerationCurrentLocked(ctx, item, epoch) {
		return false
	}
	if apply != nil {
		apply()
	}
	return true
}

func (m *Manager) fetchAccountModelsAt(ctx context.Context, item Item, epoch uint64) bool {
	if !m.withCurrentCatalog(ctx, item, epoch, nil) {
		return true // obsolete work is neither an upstream failure nor a backoff
	}
	if item.Runtime == string(providers.RuntimeInProcess) || strings.TrimSpace(item.URL) == "" {
		return m.fetchProviderModelsAt(ctx, item, epoch)
	}
	if adapter, ok := m.providers.Get(item.Provider); ok && adapter.Models != nil {
		models, err := adapter.Models.Models(ctx, item.ID)
		if err != nil {
			if !m.withCurrentCatalog(ctx, item, epoch, nil) {
				return true
			}
			var transport qoder.TransportError
			var statusErr qoder.HTTPStatusError
			switch {
			case errors.As(err, &statusErr):
				log.Printf("catalog refresh failed account=%s provider=%s stage=status status=%d body=%q", item.ID, item.Provider, statusErr.Status, statusErr.Body)
			case errors.As(err, &transport):
				log.Printf("catalog refresh failed account=%s provider=%s stage=http: %v", item.ID, item.Provider, err)
			default:
				log.Printf("catalog refresh failed account=%s provider=%s stage=decode: %v", item.ID, item.Provider, err)
			}
			return false
		}
		m.withCurrentCatalog(ctx, item, epoch, func() {
			m.pool.MergeModelsWithPricing(item.ID, qoder.CatalogIDsFromInfos(models), qoder.FreeCatalogIDsFromInfos(models))
			m.pool.MergeNativeModels(item.ID, qoder.NativeCatalogIDsFromInfos(models))
			m.mergeModelOutputLimits(item.ID, models)
		})
		return true
	}
	client := qoder.WorkerClient{
		HTTP:        &http.Client{Timeout: 15 * time.Second},
		ProxyAPIKey: m.ProxyAPIKey(),
	}
	entries, status, rawBody, err := client.Models(ctx, item.URL, false)
	if err != nil || status >= 300 {
		if !m.withCurrentCatalog(ctx, item, epoch, nil) {
			return true
		}
		m.logCatalogRefresh(item, status, rawBody, err)
		return false
	}
	m.withCurrentCatalog(ctx, item, epoch, func() { m.pool.MergeModels(item.ID, qoder.CatalogIDs(entries, nil)) })
	return true
}

func (m *Manager) logCatalogRefresh(item Item, status int, rawBody string, err error) {
	var transport qoder.TransportError
	var statusErr qoder.HTTPStatusError
	switch {
	case errors.As(err, &statusErr):
		log.Printf("catalog refresh failed account=%s provider=%s stage=status status=%d body=%q", item.ID, item.Provider, statusErr.Status, statusErr.Body)
	case errors.As(err, &transport):
		log.Printf("catalog refresh failed account=%s provider=%s stage=http: %v", item.ID, item.Provider, err)
	case err != nil && status == 0:
		log.Printf("catalog refresh failed account=%s provider=%s stage=request: %v", item.ID, item.Provider, err)
	case err != nil:
		log.Printf("catalog refresh failed account=%s provider=%s stage=decode: %v", item.ID, item.Provider, err)
	default:
		snippet := strings.TrimSpace(rawBody)
		if len(snippet) > 512 {
			snippet = snippet[:512]
		}
		log.Printf("catalog refresh failed account=%s provider=%s stage=status status=%d body=%q", item.ID, item.Provider, status, snippet)
	}
}

// fetchProviderModels fetches the catalog for an in-process provider account
// and reports whether the upstream fetch succeeded. Unsupported providers
// (no adapter, no Models capability, or in-process qoder stubs) report true:
// nothing failed, so no backoff is engaged.
func (m *Manager) fetchProviderModelsAt(ctx context.Context, item Item, epoch uint64) bool {
	if m.providers == nil {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(item.Provider), "qoder") {
		return true
	}
	adapter, ok := m.providers.Get(item.Provider)
	if !ok || adapter.Models == nil {
		return true
	}
	models, err := adapter.Models.Models(ctx, item.ID)
	if err != nil {
		if !m.withCurrentCatalog(ctx, item, epoch, nil) {
			return true
		}
		// No raw provider response or authentication material in runtime logs.
		log.Printf("catalog fetch failed account=%s provider=%s error_type=%T", item.ID, item.Provider, err)
		return false
	}
	ids := make([]string, 0, len(models)*2)
	for _, model := range models {
		ids = append(ids, model.PublicModel, model.NativeModel, model.DisplayName)
	}
	m.withCurrentCatalog(ctx, item, epoch, func() {
		m.pool.MergeModels(item.ID, ids)
		m.mergeFreeModels(item.ID, models)
	})
	return true
}

func (m *Manager) mergeFreeModels(accountID string, models []providers.ModelInfo) {
	var ids []string
	for _, model := range models {
		if model.Free {
			ids = append(ids, model.PublicModel, model.NativeModel)
		}
	}
	m.pool.MergeFreeModels(accountID, ids)
	m.mergeModelOutputLimits(accountID, models)
}

func (m *Manager) mergeModelOutputLimits(accountID string, models []providers.ModelInfo) {
	limits := map[string]int{}
	for _, model := range models {
		if limit := max(model.Capabilities.MaxOutput, model.Capabilities.MaxOutputMax); limit > 0 {
			limits[model.PublicModel] = limit
			limits[model.NativeModel] = limit
		}
	}
	m.pool.MergeOutputLimits(accountID, limits)
}
