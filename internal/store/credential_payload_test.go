package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

func TestCredentialPayloadCompareAndSwapPreservesNewLogin(t *testing.T) {
	ctx := context.Background()
	database, err := OpenStore(filepath.Join(t.TempDir(), "credential-cas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account, err := database.Create(ctx, accounts.CreateAccount{Provider: "workbuddy", Region: "cn", Name: "synthetic"})
	if err != nil {
		t.Fatal(err)
	}

	old := []byte(`{"access_token":"old","refresh_token":"old-refresh","uid":"synthetic"}`)
	login := []byte(`{"access_token":"new-login","refresh_token":"new-refresh","uid":"synthetic"}`)
	refreshed := []byte(`{"access_token":"late-refresh","refresh_token":"late-rotated","uid":"synthetic"}`)
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", old); err != nil {
		t.Fatal(err)
	}
	format, snapshot, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SaveCredentialPayload(ctx, account.ID, "workbuddy-oauth-v1", login); err != nil {
		t.Fatal(err)
	}

	saved, err := database.SaveCredentialPayloadIfCurrent(ctx, account.ID, format, snapshot, "workbuddy-oauth-v1", refreshed)
	if err != nil || saved {
		t.Fatalf("stale refresh saved=%v err=%v, want compare conflict", saved, err)
	}
	_, got, err := database.LoadCredentialPayload(ctx, account.ID)
	if err != nil || string(got) != string(login) {
		t.Fatalf("new login payload=%q err=%v", got, err)
	}

	saved, err = database.SaveCredentialPayloadIfCurrent(ctx, account.ID, "workbuddy-oauth-v1", login, "workbuddy-oauth-v1", refreshed)
	if err != nil || !saved {
		t.Fatalf("current refresh saved=%v err=%v", saved, err)
	}
}
