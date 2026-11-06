package workbuddy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/providers"
	"github.com/caigee-cmd/cli2api/internal/store"
	"github.com/caigee-cmd/cli2api/internal/translate"
)

func TestChatEvidenceTracksActualRefreshedCredential(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, newerLogin := range []bool{false, true} {
			db, err := store.OpenStore(filepath.Join(t.TempDir(), "synthetic.db"))
			if err != nil {
				t.Fatal(err)
			}
			a, err := db.Create(context.Background(), accounts.CreateAccount{Name: "synthetic", Provider: "workbuddy", Region: "cn", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			old, _ := (Credential{AccessToken: "synthetic-old", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(-time.Hour).Unix(), UID: "synthetic"}).Encode()
			if err := db.SaveCredentialPayload(context.Background(), a.ID, CredentialFormat, old); err != nil {
				t.Fatal(err)
			}
			var chatCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case pathTokenRefresh:
					_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"synthetic-new","refreshToken":"synthetic-next","expiresIn":3600}}`))
				case pathChat:
					chatCalls.Add(1)
					if r.Header.Get("Authorization") != "Bearer synthetic-new" {
						t.Error("chat did not use the refreshed synthetic credential")
					}
					if newerLogin {
						fresh, _ := (Credential{AccessToken: "synthetic-login", RefreshToken: "synthetic-login-refresh", UID: "synthetic"}).Encode()
						if err := db.SaveCredentialPayload(context.Background(), a.ID, CredentialFormat, fresh); err != nil {
							t.Error(err)
						}
					}
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"code":12153,"msg":"synthetic rejection"}`))
				default:
					http.NotFound(w, r)
				}
			}))
			client := NewClient(db)
			client.http = server.Client()
			client.http.Transport = rewriteTransport{server: server.URL, round: server.Client().Transport}
			ctx := providers.WithCredentialEvidence(context.Background(), a.ID)
			req := translate.ChatRequest{Model: "synthetic-model"}
			if stream {
				_, _, err = client.ChatStream(ctx, a.ID, req)
			} else {
				_, err = client.ChatNonStream(ctx, a.ID, req)
			}
			if err == nil || chatCalls.Load() != 1 {
				t.Fatalf("expected one synthetic 401, calls=%d err=%v", chatCalls.Load(), err)
			}
			var classified *providers.Error
			if !errors.As(err, &classified) || classified.Kind != accounts.KindAuth || classified.Status != 401 {
				t.Fatalf("missing structured auth classification: %v", err)
			}
			evidence := providers.CredentialEvidence(ctx, a.ID)
			if !evidence.Observed || !evidence.Valid {
				t.Fatalf("missing actual credential evidence: %+v", evidence)
			}
			current := db.AuthCoordinator().Version(a.ID)
			if newerLogin && evidence.Version == current {
				t.Fatal("old request inherited later login version")
			}
			if !newerLogin && evidence.Version != current {
				t.Fatalf("actual refresh version=%d current=%d", evidence.Version, current)
			}
			server.Close()
			_ = db.Close()
		}
	}
}
