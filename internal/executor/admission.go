package executor

import (
	"context"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

type routingPolicyContext struct{}
type platformOrderContext struct{}
type routingIdentity struct {
	KeyID  string
	Policy accounts.KeyRoutingPolicy
}

func WithRoutingPolicy(ctx context.Context, keyID string, policy accounts.KeyRoutingPolicy) context.Context {
	return context.WithValue(ctx, routingPolicyContext{}, routingIdentity{keyID, policy})
}

func routingIdentityFrom(ctx context.Context) routingIdentity {
	value, _ := ctx.Value(routingPolicyContext{}).(routingIdentity)
	return value
}

func platformID(provider, region string) string {
	return NormalizeProviderFamily(provider) + ":" + NormalizeRegion(region)
}

// platformOrder uses static capabilities before cooldown, budget and capacity
// filters. A temporarily blocked preferred platform cannot silently become an
// initial request to another supplier when failover is disabled.
func (p *Pool) platformOrder(q RouteQuery, identity routingIdentity) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	set := map[string]bool{}
	for _, item := range p.items {
		if !providerAllowed(item.Provider, item.Region, q.AllowedProviders) ||
			(q.ProviderFilter != "" && itemProvider(item) != NormalizeProviderFamily(q.ProviderFilter)) ||
			(q.RegionFilter != "" && itemRegion(item) != NormalizeRegion(q.RegionFilter)) ||
			(q.Eligible != nil && !q.Eligible(item)) || !itemCouldServeModel(item, q.PublicModel) {
			continue
		}
		set[platformID(item.Provider, item.Region)] = true
	}
	platforms := make([]string, 0, len(set))
	for id := range set {
		platforms = append(platforms, id)
	}
	sort.Strings(platforms)
	if len(identity.Policy.Platforms) == 0 {
		rand.Shuffle(len(platforms), func(i, j int) { platforms[i], platforms[j] = platforms[j], platforms[i] })
		return platforms
	}
	priority := func(id string) int {
		for _, row := range identity.Policy.Platforms {
			if platformID(row.Provider, row.Region) == id {
				return row.Priority
			}
		}
		return 0
	}
	sort.SliceStable(platforms, func(i, j int) bool { return priority(platforms[i]) > priority(platforms[j]) })
	if p.platformCursor == nil {
		p.platformCursor = map[string]string{}
	}
	for groupStart := 0; groupStart < len(platforms); {
		end := groupStart + 1
		for end < len(platforms) && priority(platforms[end]) == priority(platforms[groupStart]) {
			end++
		}
		cursorKey := identity.KeyID + "|" + routeModel(q.PublicModel) + "|" + itoa(priority(platforms[groupStart]))
		previous := p.platformCursor[cursorKey]
		group := append([]string(nil), platforms[groupStart:end]...)
		start := 0
		for i, id := range group {
			if id == previous {
				start = (i + 1) % len(group)
				break
			}
		}
		for i := range group {
			platforms[groupStart+i] = group[(i+start)%len(group)]
		}
		if len(p.platformCursor) >= rotationLimit {
			p.platformCursor = map[string]string{}
		}
		p.platformCursor[cursorKey] = platforms[groupStart]
		groupStart = end
	}

	return platforms
}

func splitPlatform(id string) (string, string) {
	provider, region, _ := strings.Cut(id, ":")
	return provider, region
}

// acquireRoute selects and reserves both limits in one critical section.
// The returned release is idempotent, including after account removal.
func (p *Pool) acquireRoute(q RouteQuery, identity routingIdentity) (Item, func(), bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := identity.KeyID + "|" + platformID(q.ProviderFilter, q.RegionFilter)
	limit := 0
	for _, row := range identity.Policy.Platforms {
		if platformID(row.Provider, row.Region) == platformID(q.ProviderFilter, q.RegionFilter) {
			limit = row.MaxInFlight
			break
		}
	}
	if limit > 0 && p.keyPlatformLive[key] >= limit {
		return Item{}, nil, false
	}
	item, ok := p.pickRouteLocked(q)
	item = item.clone()
	if !ok {
		return Item{}, nil, false
	}
	if resumeAt(item, q).After(time.Now()) || (item.Quota != nil && item.Quota.Exceeded && !itemModelFree(item, q.PublicModel)) {
		return item, func() {}, true
	}
	if p.keyPlatformLive == nil {
		p.keyPlatformLive = map[string]int{}
	}
	p.keyPlatformLive[key]++
	if p.accountLive == nil {
		p.accountLive = map[string]int{}
	}
	p.accountLive[item.ID]++
	for i := range p.items {
		if p.items[i].ID == item.ID {
			p.items[i].LiveInFlight = p.accountLive[item.ID]
			item.LiveInFlight = p.items[i].LiveInFlight
			break
		}
	}
	var once sync.Once
	return item, func() {
		once.Do(func() {
			p.mu.Lock()
			if p.keyPlatformLive[key] > 1 {
				p.keyPlatformLive[key]--
			} else {
				delete(p.keyPlatformLive, key)
			}
			if p.accountLive[item.ID] > 1 {
				p.accountLive[item.ID]--
			} else {
				delete(p.accountLive, item.ID)
			}
			for i := range p.items {
				if p.items[i].ID == item.ID {
					p.items[i].LiveInFlight = p.accountLive[item.ID]
					break
				}
			}
			p.mu.Unlock()
			p.signalCapacityChanged()
		})
	}, true
}

func itemModelFree(item Item, model string) bool {
	return itemServesFreeModel(item, model)
}

func (p *Pool) MergeFreeModels(id string, models []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.items {
		if p.items[i].ID == id {
			p.items[i].FreeModels = append([]string{}, models...)
			return
		}
	}
}

func (p *Pool) MergeOutputLimits(id string, limits map[string]int) {
	copy := map[string]int{}
	for model, limit := range limits {
		if limit > 0 {
			copy[routeModel(model)] = limit
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.items {
		if p.items[i].ID == id {
			p.items[i].ModelOutputLimits = copy
			return
		}
	}
}

func (p *Pool) PlatformInFlight() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make(map[string]int, len(p.keyPlatformLive))
	for key, value := range p.keyPlatformLive {
		result[key] = value
	}
	return result
}
