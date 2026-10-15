package gateway

import (
	"errors"

	"github.com/caigee-cmd/cli2api/internal/accounts"
	"github.com/caigee-cmd/cli2api/internal/executor"
)

// Finalize before deferred body.Close: the executor's close fallback cannot
// distinguish a complete response from an interrupted stream without relay facts.
func finalizeStream(upstream executor.StreamResult, stats StreamRelayStats, status string, err error) {
	if upstream.Finalize == nil {
		return
	}
	completed := status == accounts.RequestStatusOK
	if !completed && err == nil {
		err = errors.New("upstream stream ended without successful completion")
	}
	credits := stats.ConsumedCredits
	if credits == nil {
		credits = stats.Credits
	}
	upstream.Finalize(executor.StreamCompletion{
		PromptTokens: stats.PromptTokens, CompletionTokens: stats.CompletionTokens,
		UsageSource: stats.UsageSource, Credits: credits, Err: err, Completed: completed,
	})
}
