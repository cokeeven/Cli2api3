package console

import (
	"net/http"
	"strings"

	"github.com/caigee-cmd/cli2api/internal/executor"
)

func (h *Handler) HandleChat(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.Chat == nil {
		writeErr(w, http.StatusServiceUnavailable, "chat_unavailable", "chat handler unavailable")
		return
	}
	if accountID := strings.TrimSpace(h.requestedAccount(r)); accountID != "" {
		r = r.WithContext(executor.WithStrictAccount(r.Context(), accountID))
	}
	h.Chat(w, r)
}
