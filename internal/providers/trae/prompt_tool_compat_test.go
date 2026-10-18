package trae

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestPrepareBodyMapsDeveloperToSupportedInstructionRole(t *testing.T) {
	raw := PrepareBody([]byte(`{"messages":[{"role":"system","content":"original"},{"role":"developer","content":"caller rule"},{"role":"user","content":"ping"}]}`), PrimaryScene)
	var body struct {
		Messages []struct {
			Role    string
			Content []struct{ Text string }
		}
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 3 || body.Messages[0].Role != "system" || body.Messages[1].Role != "system" || body.Messages[1].Content[0].Text != "caller rule" || body.Messages[2].Role != "user" {
		t.Fatalf("lost caller instructions: %s", raw)
	}
}

func TestToolCallsFinishReasonMatchesOpenAI(t *testing.T) {
	for _, reason := range []string{"", "stop", "length"} {
		t.Run(reason, func(t *testing.T) {
			done := `{"finish_reason":"` + reason + `"}`
			if reason == "" {
				done = `{}`
			}
			raw := "event: output\ndata: {\"tool_calls\":[{\"index\":0,\"id\":\"call_a\",\"function_call\":{\"name\":\"echo\",\"arguments\":\"{}\"}}]}\n\nevent: done\ndata: " + done + "\n\n"
			want := reason
			if reason == "" || reason == "stop" {
				want = "tool_calls"
			}
			obj, err := Aggregate(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if got := obj["choices"].([]map[string]any)[0]["finish_reason"]; got != want {
				t.Fatalf("finish=%v want=%s object=%#v", got, want, obj)
			}
			if len(obj["choices"].([]map[string]any)[0]["message"].(map[string]any)["tool_calls"].([]any)) != 1 {
				t.Fatalf("non-stream tool call lost or repeated: %#v", obj)
			}
			src := io.NopCloser(strings.NewReader(raw))
			resp, _ := startSoloRewrite(src, src, "test")
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), `"finish_reason":"`+want+`"`) {
				t.Fatalf("stream finish missing: %s", data)
			}
			if strings.Count(string(data), `"id":"call_a"`) != 1 || strings.Contains(string(data), `"content"`) {
				t.Fatalf("tool-only stream lost/repeated call or invented body content: %s", data)
			}
		})
	}
}
