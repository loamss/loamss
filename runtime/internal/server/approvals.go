package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/loamss/loamss/runtime/internal/permission"
)

// Approval workflow under /console/approvals.
//
//   POST /console/approvals/{id}/approve  — grants the request
//   POST /console/approvals/{id}/deny     — denies the request
//
// This is the design's most important surface: the moment a capsule
// or client asks for a capability that wasn't pre-granted, the
// runtime holds the request and waits for a human decision. The
// dashboard's Approvals pane reads pending approvals via
// /console/state and posts decisions here.
//
// Body shape: { note?: string } — an optional reason the user
// types. Persisted on the resolved approval and emitted in the
// audit log. Empty body is fine; the decision itself is the
// payload.
//
// Same unauthenticated-localhost contract as the rest of /console/*.

type approvalDecisionRequest struct {
	Note string `json:"note,omitempty"`
}

type approvalDecisionResponse struct {
	OK       bool   `json:"ok"`
	ID       string `json:"id"`
	Decision string `json:"decision"` // "granted" or "denied"
}

func (s *Server) handleApprovalApprove(w http.ResponseWriter, r *http.Request) {
	s.resolveApproval(w, r, permission.ApprovalGranted)
}

func (s *Server) handleApprovalDeny(w http.ResponseWriter, r *http.Request) {
	s.resolveApproval(w, r, permission.ApprovalDenied)
}

// resolveApproval shares the bulk of approve/deny — the only
// difference between the two paths is the ApprovalState value.
// Keeping them as separate exported handlers (not query-param-
// driven) makes the audit trail unambiguous: every approve hit
// /approve, every deny hit /deny, and routers / WAFs can apply
// different policies to each.
func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request, decision permission.ApprovalState) {
	if s.engine == nil {
		writeJSONError(w, "approval workflow is not enabled in this build", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSONError(w, "approval id required in URL", http.StatusBadRequest)
		return
	}

	// Empty body is OK (no note); enforce a small size cap.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil {
		writeJSONError(w, "request body too large or unreachable", http.StatusBadRequest)
		return
	}
	var req approvalDecisionRequest
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
	}

	ctx := r.Context()
	err = s.engine.ResolveApproval(ctx, id, decision, "console", req.Note)
	switch {
	case errors.Is(err, permission.ErrApprovalNotFound):
		writeJSONError(w, "no approval with that id", http.StatusNotFound)
		return
	case errors.Is(err, permission.ErrApprovalAlreadyResolved):
		// The approval has already moved out of pending — possibly
		// because another decider raced us, or the user clicked the
		// button twice. 409 so the dashboard can refetch + refresh.
		writeJSONError(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		s.logger.Warn("console approval resolve failed", "err", err, "id", id, "decision", decision)
		writeJSONError(w, "approval resolution failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	s.logger.Info("console approval resolved",
		"id", id, "decision", decision, "note_len", len(req.Note))
	writeJSON(w, http.StatusOK, approvalDecisionResponse{
		OK:       true,
		ID:       id,
		Decision: string(decision),
	})
}
