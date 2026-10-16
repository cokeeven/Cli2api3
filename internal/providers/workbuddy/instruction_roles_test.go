package workbuddy

import (
	"encoding/json"
	"testing"
)

func TestPrepareBodyPreservesDeveloperInstructionsUsingSystemRole(t *testing.T) {
	for _, prefix := range []string{"", `{"role":"system","content":"caller system"},`} {
		out := PrepareBody([]byte(`{"messages":[` + prefix + `{"role":"developer","content":"caller developer","name":"rules"},{"role":"user","content":"ping"}]}`))
		var body struct {
			Messages []struct{ Role, Content, Name string }
		}
		if err := json.Unmarshal(out, &body); err != nil {
			t.Fatal(err)
		}
		want := 2
		if prefix != "" {
			want++
		}
		if len(body.Messages) != want {
			t.Fatalf("unexpected injection or loss: %s", out)
		}
		instruction := body.Messages[want-2]
		if instruction.Role != "system" || instruction.Content != "caller developer" || instruction.Name != "rules" || body.Messages[want-1].Role != "user" {
			t.Fatalf("lost caller instruction: %s", out)
		}
	}
}
