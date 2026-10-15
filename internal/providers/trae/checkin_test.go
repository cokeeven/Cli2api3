package trae

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// seedCheckinCredential stores a long-lived Trae credential so Checkin does
// not try to refresh it against the test server.
func seedCheckinCredential(t *testing.T, store *memStore) {
	t.Helper()
	payload, err := json.Marshal(Credential{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    4102444800,
		Domain:       DomainCN,
		UID:          "u1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCredentialPayload(context.Background(), "acc1", CredentialFormat, payload); err != nil {
		t.Fatal(err)
	}
}

func TestCheckinClaimFlow(t *testing.T) {
	var paths []string
	var statusCalls int
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if auth := r.Header.Get("Authorization"); auth != "Cloud-IDE-JWT at" {
			t.Fatalf("authorization=%q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.TrimSpace(string(body)) != `{"req_source":1}` {
			t.Fatalf("body=%q", body)
		}
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case pathCheckinStatus:
			statusCalls++
			// First probe: not yet checked in. Re-probe after claim: flipped.
			if statusCalls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "checked_in": false, "credits": 0, "enable": true})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "checked_in": true, "credits": 200})
			}
		case pathCheckinClaim:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	seedCheckinCredential(t, store)

	result, err := client.Checkin(context.Background(), "acc1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" {
		t.Fatalf("result=%+v", result)
	}
	if result.RewardCredits != 200 {
		t.Fatalf("reward=%v", result.RewardCredits)
	}
	if len(paths) != 3 || paths[0] != pathCheckinStatus || paths[1] != pathCheckinClaim || paths[2] != pathCheckinStatus {
		t.Fatalf("paths=%v", paths)
	}
}

func TestCheckinAlreadyClaimedUpstream(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "checked_in": true, "credits": 100})
	}))
	seedCheckinCredential(t, store)

	result, err := client.Checkin(context.Background(), "acc1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "already" {
		t.Fatalf("result=%+v", result)
	}
	if result.RewardCredits != 0 {
		t.Fatalf("already must not report a new reward: %+v", result)
	}
}

func TestCheckinDoesNotInferAlreadyFromBusinessMessage(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 10001, "msg": "今日已签到，请明天再来"})
	}))
	seedCheckinCredential(t, store)

	result, err := client.Checkin(context.Background(), "acc1")
	if err == nil || result.Valid() {
		t.Fatalf("unconfirmed business message accepted: result=%+v err=%v", result, err)
	}
}

func TestCheckinClaimAlreadyRace(t *testing.T) {
	statusCalls := 0
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case pathCheckinStatus:
			statusCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "checked_in": statusCalls > 1, "enable": true, "credits": 100})
		case pathCheckinClaim:
			// Claim 与 status 之间出现 race: 状态查询时还已签到未发生,
			// 到 claim 时已经被其它通道签过,业务方返回 already 提示。
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 9095, "msg": "already checked in today"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	seedCheckinCredential(t, store)

	result, err := client.Checkin(context.Background(), "acc1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "already" {
		t.Fatalf("result=%+v", result)
	}
	if statusCalls != 2 || result.RewardCredits != 0 {
		t.Fatalf("race must confirm state without new reward: calls=%d result=%+v", statusCalls, result)
	}
}

func TestCheckinDisabled(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "checked_in": false, "enable": false})
	}))
	seedCheckinCredential(t, store)

	result, err := client.Checkin(context.Background(), "acc1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "skipped" {
		t.Fatalf("result=%+v", result)
	}
}

func TestCheckinAuthDead(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "1001", "msg": "unauthorized"})
	}))
	seedCheckinCredential(t, store)

	_, err := client.Checkin(context.Background(), "acc1")
	if err == nil {
		t.Fatal("auth-dead must fail")
	}
	var already AlreadyCheckedInError
	if errors.As(err, &already) {
		t.Fatalf("auth-dead must not map to already: %v", err)
	}
}

func TestCheckinRegisteredOnAdapter(t *testing.T) {
	client, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	adapter := client.Adapter()
	if adapter.Checkin == nil {
		t.Fatal("checkin must be registered on the trae adapter")
	}
}

func TestCheckinRejectsUnconfirmedStatusBeforeClaim(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `[]`, `{"code":0}`, `{"checked_in":null,"enable":true}`,
		`{"checked_in":true,"enable":true}`, `{"checked_in":false,"enable":false}`,
		`{"checked_in":false}`, `{"checked_in":false,"enable":null}`,
		`{"checked_in":"true","enable":true}`, `{"checked_in":false,"enable":"false"}`,
		`{"code":9074,"checked_in":true,"enable":true}`,
		`{"code":9095,"message":"already checked in"}`,
		`{"code":null,"checked_in":true}`, `{"code":false,"checked_in":true}`,
		`{"checked_in":true,"credits":-1}`, `{"checked_in":true,"extra_credits":-1}`,
		`{"checked_in":true,"credits":9223372036854775807,"extra_credits":1}`,
		`{"message":"not already checked in; status unavailable"}`,
	} {
		t.Run(body, func(t *testing.T) {
			claims := 0
			client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == pathCheckinClaim {
					claims++
				}
				_, _ = io.WriteString(w, body)
			}))
			seedCheckinCredential(t, store)
			result, err := client.Checkin(context.Background(), "acc1")
			if err == nil || result.Valid() || claims != 0 {
				t.Fatalf("status accepted: result=%+v claims=%d err=%v", result, claims, err)
			}
		})
	}
}

func TestCheckinHTTPFailureDoesNotPersistUpstreamBody(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"code":9074,"message":"already checked in; secret-access-token"}`)
	}))
	seedCheckinCredential(t, store)
	_, err := client.Checkin(context.Background(), "acc1")
	if err == nil || strings.Contains(err.Error(), "secret-access-token") || strings.Contains(err.Error(), "already checked") {
		t.Fatalf("upstream error must be safe and not already: %v", err)
	}
}

func TestCheckinRejectsUnconfirmedClaim(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"code":null}`, `{"code":false}`, `{"code":0.5}`, `{"code":9074,"message":"already checked in"}`, `{"code":10001,"message":"今日已签到"}`, `{"message":"already checked in today"}`} {
		t.Run(body, func(t *testing.T) {
			claims, statuses := 0, 0
			client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == pathCheckinClaim {
					claims++
					_, _ = io.WriteString(w, body)
					return
				}
				statuses++
				_, _ = io.WriteString(w, `{"code":0,"checked_in":false,"enable":true}`)
			}))
			seedCheckinCredential(t, store)
			result, err := client.Checkin(context.Background(), "acc1")
			if err == nil || result.Valid() || claims != 1 || statuses != 1 {
				t.Fatalf("claim accepted: result=%+v claims=%d statuses=%d err=%v", result, claims, statuses, err)
			}
		})
	}
}

func TestCheckinClaimRequiresReadback(t *testing.T) {
	for _, claim := range []string{`{"code":0}`, `{"code":"0"}`, `{"code":9095}`} {
		for _, after := range []string{`{}`, `{"code":0,"checked_in":false,"enable":true}`, `{"code":9074,"checked_in":true}`, `{"code":9095,"message":"already checked in"}`} {
			t.Run(claim+after, func(t *testing.T) {
				statuses, claims := 0, 0
				client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == pathCheckinClaim {
						claims++
						_, _ = io.WriteString(w, claim)
						return
					}
					statuses++
					if statuses == 1 {
						_, _ = io.WriteString(w, `{"code":0,"checked_in":false,"enable":true}`)
						return
					}
					_, _ = io.WriteString(w, after)
				}))
				seedCheckinCredential(t, store)
				result, err := client.Checkin(context.Background(), "acc1")
				if err == nil || result.Valid() || statuses != 2 || claims != 1 {
					t.Fatalf("readback accepted: result=%+v statuses=%d claims=%d err=%v", result, statuses, claims, err)
				}
			})
		}
	}
}

func TestCheckinAlreadyStateDoesNotPostClaim(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathCheckinStatus {
			t.Fatalf("already account attempted claim")
		}
		_, _ = io.WriteString(w, `{"code":0,"checked_in":true,"did_checked_in":false,"enable":true,"credits":100,"extra_credits":100}`)
	}))
	seedCheckinCredential(t, store)
	result, err := client.Checkin(context.Background(), "acc1")
	if err != nil || result.Status != "already" || result.RewardCredits != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCheckinDeviceAlreadyUsedDoesNotClaimForAccount(t *testing.T) {
	client, store := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pathCheckinStatus {
			t.Fatalf("device-limited account attempted claim")
		}
		_, _ = io.WriteString(w, `{"code":0,"checked_in":false,"did_checked_in":true,"enable":true,"credits":100,"extra_credits":100}`)
	}))
	seedCheckinCredential(t, store)
	result, err := client.Checkin(context.Background(), "acc1")
	if err != nil || result.Status != "skipped" || result.RewardCredits != 0 || !strings.Contains(result.Message, "device") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
