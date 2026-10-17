// Test-only fictional database. Not included in application packages.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/store"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	var directory string
	flag.StringVar(&directory, "data", "", "isolated data directory")
	flag.Parse()
	if !filepath.IsAbs(directory) || !strings.HasPrefix(filepath.Base(filepath.Dir(directory)), "isolated-") {
		panic("fixture directory must be inside a generated isolated-* root")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		panic("fixture data directory must not exist")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		panic(err)
	}
	db, err := store.OpenStore(filepath.Join(directory, "qoder.db"))
	if err != nil {
		panic(err)
	}
	defer db.Close()
	ctx := context.Background()
	a, err := db.Create(ctx, accounts.CreateAccount{Name: "Fictional onboarding", Provider: "trae", Region: "cn", Enabled: false})
	if err != nil {
		panic(err)
	}
	if err = db.SaveCredentialPayload(ctx, a.ID, "trae-oauth-v1", []byte(`{"fake":"fictional-onboarding"}`)); err != nil {
		panic(err)
	}
	if err = db.SetSecret(ctx, "proxy_api_key", "fictional-onboarding-console-key"); err != nil {
		panic(err)
	}
	if _, err = db.InsertAPIKey(ctx, accounts.StoredAPIKey{ID: "fictional-onboarding-key", Name: "fixture", Prefix: "fake", KeyHash: "fictional-only", Enabled: true}); err != nil {
		panic(err)
	}
	if err = db.SetModelContext(ctx, "fictional-onboarding-model", 12345); err != nil {
		panic(err)
	}
	if err = db.InsertRequestLog(ctx, accounts.RequestLog{ID: "fictional-onboarding-log", AccountID: a.ID, Provider: "trae", Status: "ok", CreatedAt: time.Now()}); err != nil {
		panic(err)
	}
	if err = db.RecordCheckin(ctx, a.ID, "success", "fictional history", time.Now()); err != nil {
		panic(err)
	}
	fmt.Println("Seeded fictional onboarding state")
}
