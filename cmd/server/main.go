package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/caigee-cmd/cli2api/internal/app"
	"github.com/caigee-cmd/cli2api/internal/config"
	"github.com/joho/godotenv"
)

func main() {
	if f := os.Getenv("CLI2API_ENV_FILE"); f != "" {
		if err := godotenv.Load(f); err != nil {
			log.Fatal("cannot read configured environment file")
		}
	} else {
		_ = godotenv.Load()
	}
	closeJob, err := nativeWorkerJob()
	if err != nil {
		log.Fatal(err)
	}
	defer closeJob()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	application := app.New(cfg)
	defer application.Close()
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	shutdown := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/update/shutdown" {
			host, _, _ := net.SplitHostPort(r.RemoteAddr)
			ip := net.ParseIP(host)
			token := strings.TrimSpace(cfg.UpdateAgentToken)
			if r.Method != http.MethodPost || ip == nil || !ip.IsLoopback() || token == "" || os.Getenv("UPDATE_INSTANCE_ID") == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			select {
			case shutdown <- struct{}{}:
			default:
			}
			return
		}
		application.Handler().ServeHTTP(w, r)
	})
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("cli2api listening on http://%s", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("http server failed: %v", err)
			stop()
		}
	}()
	select {
	case <-ctx.Done():
	case <-shutdown:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown failed: %v", err)
	}
}
