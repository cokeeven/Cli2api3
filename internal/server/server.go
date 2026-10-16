package server

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/caigee-cmd/cli2api/internal/auth"
	appconsole "github.com/caigee-cmd/cli2api/internal/console"
	apigateway "github.com/caigee-cmd/cli2api/internal/gateway"
	appupdate "github.com/caigee-cmd/cli2api/internal/update"
)

// Server is the HTTP surface: CORS, OPTIONS, maintenance, mux, auth wrapping,
// and webui fallback. It does not construct Store or Manager.
type Server struct {
	Auth          auth.Verifier
	Gateway       *apigateway.Handler
	Console       *appconsole.Handler
	Update        *appupdate.Coordinator
	TouchKey      func(ctx context.Context, keyID string)
	CrossProvider *atomic.Bool

	// Admission and drain observation share a lock: no request can pass the
	// maintenance check and become invisible to a concurrent drain.
	inflight *requestTracker

	mux *http.ServeMux
}

type requestTracker struct {
	mu     sync.Mutex
	active int64
	idle   chan struct{}
}

func New(cfg Server) *Server {
	s := cfg
	if s.mux == nil {
		s.mux = http.NewServeMux()
	}
	if s.inflight == nil {
		s.inflight = &requestTracker{}
	}
	out := s
	out.routes()
	return &out
}

// Drain waits until no request is being served, or until ctx expires. It
// reports whether the server became idle.
func (s *Server) Drain(ctx context.Context) bool {
	if s == nil {
		return true
	}
	group := s.inflight
	if group == nil {
		return true
	}
	group.mu.Lock()
	if group.active == 0 {
		group.mu.Unlock()
		return true
	}
	done := group.idle
	group.mu.Unlock()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

// ActiveRequests reports how many requests are being served right now.
func (s *Server) ActiveRequests() int64 {
	if s == nil || s.inflight == nil {
		return 0
	}
	s.inflight.mu.Lock()
	defer s.inflight.mu.Unlock()
	return s.inflight.active
}

func (s *Server) admitRequest() bool {
	group := s.inflight
	if group == nil {
		return !s.updater().Maintenance.Load()
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if s.updater().Maintenance.Load() {
		return false
	}
	if group.active == 0 {
		group.idle = make(chan struct{})
	}
	group.active++
	return true
}

func (s *Server) finishRequest() {
	group := s.inflight
	if group == nil {
		return
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	group.active--
	if group.active == 0 {
		close(group.idle)
	}
}

func (s *Server) updater() *appupdate.Coordinator {
	if s == nil || s.Update == nil {
		return &appupdate.Coordinator{}
	}
	return s.Update
}

func (s *Server) gatewayHandler() *apigateway.Handler {
	if s == nil || s.Gateway == nil {
		return &apigateway.Handler{}
	}
	return s.Gateway
}

func (s *Server) consoleHandler() *appconsole.Handler {
	if s == nil || s.Console == nil {
		return &appconsole.Handler{}
	}
	return s.Console
}

func (s *Server) crossProviderOn() bool {
	return s != nil && s.CrossProvider != nil && s.CrossProvider.Load()
}
