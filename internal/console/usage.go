package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/caigee-cmd/cli2api/internal/accounts"
)

// HandleAccountUsage is registered behind the same console authentication as
// all /api/accounts routes. Settings persistence stays in control.
func (h *Handler) HandleAccountUsage(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/accounts/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 || (parts[1] != "usage" && parts[1] != "usage-policy" && parts[1] != "usage-reconcile") {
		writeErr(w, http.StatusNotFound, "not_found", "unknown account usage route")
		return
	}
	id := parts[0]
	if h.Control == nil || h.Control.Usage == nil {
		writeErr(w, http.StatusServiceUnavailable, "usage_unavailable", "usage service unavailable")
		return
	}
	if parts[1] == "usage-reconcile" {
		if r.Method != http.MethodPost {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
			return
		}
		var input struct {
			SnapshotFetchedAt string `json:"snapshot_fetched_at"`
			ConfirmCovered    bool   `json:"confirm_covered"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConsoleBodyBytes)).Decode(&input); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if !input.ConfirmCovered {
			writeErr(w, http.StatusBadRequest, "confirmation_required", "confirm_covered must be true")
			return
		}
		count, err := h.Control.Usage.ReconcileBalance(r.Context(), id, input.SnapshotFetchedAt)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "reconcile_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"covered_attempts": count})
		return
	}
	if parts[1] == "usage" {
		if r.Method != http.MethodGet {
			writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
			return
		}
		days, _ := strconv.Atoi(r.URL.Query().Get("days"))
		out, err := h.Control.Usage.Analysis(r.Context(), id, days)
		if err != nil {
			h.writeUsageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	switch r.Method {
	case http.MethodGet:
		p, err := h.Control.Usage.Policy(r.Context(), id)
		if err != nil {
			h.writeUsageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, p)
	case http.MethodPut:
		var p accounts.UsagePolicy
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxConsoleBodyBytes)).Decode(&p); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		normalized, err := accounts.NormalizeUsagePolicy(p)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_usage_policy", err.Error())
			return
		}
		if err = h.Control.Usage.SetPolicy(r.Context(), id, normalized); err != nil {
			h.writeUsageError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, normalized)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or PUT only")
	}
}
func (h *Handler) writeUsageError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, accounts.ErrAccountNotFound) {
		status = http.StatusNotFound
	}
	writeErr(w, status, "usage_failed", err.Error())
}
