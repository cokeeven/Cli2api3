package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckinWorkerRewardJSONCompatibility(t *testing.T) {
	for _, tc := range []struct {
		payload, want string
		absent        bool
	}{
		{`{"ok":true,"status":"success","reward_credits":100}`, `"reward_credits":100`, false},
		{`{"ok":true,"status":"success","reward_credits":0}`, `"reward_credits"`, true},
		{`{"ok":true,"status":"success"}`, `"reward_credits"`, true},
	} {
		t.Run(tc.payload, func(t *testing.T) {
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.payload) }))
			defer worker.Close()
			c := NewClient()
			c.Bind(func(string) (string, bool) { return worker.URL, true }, func() string { return "local-key" })
			result, err := c.Checkin(context.Background(), "a")
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), tc.want) == tc.absent {
				t.Fatalf("json=%s", encoded)
			}
		})
	}
}
