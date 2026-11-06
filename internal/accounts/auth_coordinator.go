package accounts

import "sync"

// AuthCoordinator orders authentication events for one account. Locks are
// never held over upstream I/O; callers acquire an account lock only for a
// state snapshot or for a conditional local commit. Different accounts use
// different locks. Entries remain allocated for the process lifetime so an
// old in-flight request cannot acquire a different lock after account removal.
type AuthCoordinator struct {
	mu        sync.Mutex
	accounts  map[string]*authEntry
	hookMu    sync.RWMutex
	recovered func(string, uint64, bool)
	changed   func(string, uint64)
}

type authEntry struct {
	mu        sync.Mutex
	version   uint64
	poolEvent bool
}

type AuthGuard struct{ entry *authEntry }

func (c *AuthCoordinator) Lock(id string) *AuthGuard {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.accounts == nil {
		c.accounts = make(map[string]*authEntry)
	}
	e := c.accounts[id]
	if e == nil {
		e = &authEntry{}
		c.accounts[id] = e
	}
	c.mu.Unlock()
	e.mu.Lock()
	return &AuthGuard{entry: e}
}

func (g *AuthGuard) Version() uint64 {
	if g == nil {
		return 0
	}
	return g.entry.version
}
func (g *AuthGuard) Advance() uint64 {
	if g == nil {
		return 0
	}
	g.entry.version++
	g.entry.poolEvent = false
	return g.entry.version
}
func (g *AuthGuard) AdvancePool() uint64 {
	if g == nil {
		return 0
	}
	g.entry.version++
	g.entry.poolEvent = true
	return g.entry.version
}
func (g *AuthGuard) PoolEvent() bool { return g != nil && g.entry.poolEvent }
func (g *AuthGuard) Unlock() {
	if g != nil {
		g.entry.mu.Unlock()
	}
}

func (c *AuthCoordinator) Version(id string) uint64 {
	g := c.Lock(id)
	defer g.Unlock()
	return g.Version()
}

type AuthCoordinatorSource interface{ AuthCoordinator() *AuthCoordinator }

// SetRecoveryHook is installed once by the runtime. The hook runs under the
// account guard, must not acquire it again, and must not perform network I/O.
func (c *AuthCoordinator) SetRecoveryHook(hook func(string, uint64, bool)) {
	if c == nil {
		return
	}
	c.hookMu.Lock()
	c.recovered = hook
	c.hookMu.Unlock()
}
func (c *AuthCoordinator) Recovered(id string, version uint64, wasAuth bool) {
	if c == nil {
		return
	}
	c.hookMu.RLock()
	hook := c.recovered
	c.hookMu.RUnlock()
	if hook != nil {
		hook(id, version, wasAuth)
	}
}

func (c *AuthCoordinator) SetChangeHook(hook func(string, uint64)) {
	if c == nil {
		return
	}
	c.hookMu.Lock()
	c.changed = hook
	c.hookMu.Unlock()
}
func (c *AuthCoordinator) Changed(id string, version uint64) {
	if c == nil {
		return
	}
	c.hookMu.RLock()
	hook := c.changed
	c.hookMu.RUnlock()
	if hook != nil {
		hook(id, version)
	}
}
