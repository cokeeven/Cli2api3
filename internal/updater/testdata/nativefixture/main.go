// A real Windows child process for isolated updater failure/recovery rehearsals.
package main

import (
	"context"
	"encoding/json"
	"github.com/caigee-cmd/cli2api/internal/store"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var version = "v9.0.1"
var commit = "1111111111111111111111111111111111111111"

func main() {
	mode := os.Getenv("NATIVE_FIXTURE_MODE")
	if version == "v9.0.2" && mode == "exit" {
		os.Exit(1)
	}
	db, err := store.OpenStore(filepath.Join(os.Getenv("QODER_DATA_DIR"), "qoder.db"))
	if err != nil {
		os.Exit(2)
	}
	defer db.Close()
	if version == "v9.0.1" && mode == "schema-fail" {
		var n int
		if db.DB().QueryRow("SELECT COUNT(*) FROM native_test_records").Scan(&n) == nil && n > 0 {
			os.Exit(3)
		}
	}
	if version == "v9.0.2" && (mode == "write-fail" || mode == "schema-fail" || mode == "crash-after-write") {
		_, _ = db.DB().Exec("CREATE TABLE IF NOT EXISTS native_test_records (value TEXT)")
		_, _ = db.DB().Exec("INSERT INTO native_test_records(value) VALUES ('after-upgrade')")
	}
	if version == "v9.0.2" && mode == "crash-after-write" {
		go func() { time.Sleep(250 * time.Millisecond); os.Exit(4) }()
	}
	stop := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": !(version == "v9.0.2" && (mode == "write-fail" || mode == "schema-fail")), "version": version, "commit": commit, "instance_id": os.Getenv("UPDATE_INSTANCE_ID"), "pid": os.Getpid()})
	})
	mux.HandleFunc("/internal/update/shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+os.Getenv("UPDATE_AGENT_TOKEN") {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(202)
		select {
		case stop <- struct{}{}:
		default:
		}
	})
	server := &http.Server{Addr: "127.0.0.1:" + os.Getenv("PORT"), Handler: mux}
	go func() {
		if server.ListenAndServe() != http.ErrServerClosed {
			select {
			case stop <- struct{}{}:
			default:
			}
		}
	}()
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
