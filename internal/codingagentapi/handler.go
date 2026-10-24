package codingagentapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/codingagent"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

type Handler struct {
	token string
	store Repository
}

type Repository interface {
	EnsureCodingTaskManifest(context.Context, string, string, string) (codingagent.TaskManifest, error)
	SaveCodingAgentEvidence(context.Context, codingagent.Evidence) error
	EnsureCodingSession(context.Context, string) (codingagent.CodingSession, error)
	GetCodingSession(context.Context, string) (codingagent.CodingSession, error)
	PendingCodingRuns(context.Context, string) ([]codingagent.AgentRun, error)
	StartCodingAgentRun(context.Context, string, string) (codingagent.AgentRun, error)
	CompleteCodingAgentRun(context.Context, string, codingagent.RunCompletion) (codingagent.CodingSession, error)
	FailCodingAgentRun(context.Context, string, codingagent.RunFailure) (codingagent.CodingSession, error)
}

func New(token string, repository Repository) *Handler {
	return &Handler{token: token, store: repository}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /coding-agent/dispatches/{dispatch}/manifest", h.manifest)
	mux.HandleFunc("POST /coding-agent/dispatches/{dispatch}/evidence", h.evidence)
	mux.HandleFunc("POST /coding-agent/dispatches/{dispatch}/sessions", h.session)
	mux.HandleFunc("GET /coding-agent/sessions/{session}", h.getSession)
	mux.HandleFunc("GET /coding-agent/sessions/{session}/runs/pending", h.pendingRuns)
	mux.HandleFunc("POST /coding-agent/runs/{run}/start", h.startRun)
	mux.HandleFunc("POST /coding-agent/runs/{run}/complete", h.completeRun)
	mux.HandleFunc("POST /coding-agent/runs/{run}/fail", h.failRun)
}

func (h *Handler) session(writer http.ResponseWriter, request *http.Request) {
	if !h.authorize(request) {
		http.Error(writer, "invalid coding-agent token", http.StatusUnauthorized)
		return
	}
	dispatchID := request.PathValue("dispatch")
	if _, err := h.store.EnsureCodingTaskManifest(request.Context(), dispatchID, "pi", ""); err != nil {
		http.Error(writer, err.Error(), statusFor(err))
		return
	}
	session, err := h.store.EnsureCodingSession(request.Context(), dispatchID)
	if err != nil {
		http.Error(writer, err.Error(), statusFor(err))
		return
	}
	writeJSON(writer, http.StatusOK, session)
}

func (h *Handler) getSession(writer http.ResponseWriter, request *http.Request) {
	if !h.authorize(request) {
		http.Error(writer, "invalid coding-agent token", http.StatusUnauthorized)
		return
	}
	session, err := h.store.GetCodingSession(request.Context(), request.PathValue("session"))
	if err != nil {
		http.Error(writer, err.Error(), statusFor(err))
		return
	}
	writeJSON(writer, http.StatusOK, session)
}

func (h *Handler) pendingRuns(writer http.ResponseWriter, request *http.Request) {
	if !h.authorize(request) {
		http.Error(writer, "invalid coding-agent token", http.StatusUnauthorized)
		return
	}
	runs, err := h.store.PendingCodingRuns(request.Context(), request.PathValue("session"))
	if err != nil {
		http.Error(writer, err.Error(), statusFor(err))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"runs": runs})
}

func (h *Handler) startRun(writer http.ResponseWriter, request *http.Request) {
	if !h.authorize(request) {
		http.Error(writer, "invalid coding-agent token", http.StatusUnauthorized)
		return
	}
	var body struct {
		ProviderSessionID string `json:"provider_session_id"`
	}
	if err := decodeBody(request, &body); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	run, err := h.store.StartCodingAgentRun(request.Context(), request.PathValue("run"), body.ProviderSessionID)
	if err != nil {
		http.Error(writer, err.Error(), statusFor(err))
		return
	}
	writeJSON(writer, http.StatusOK, run)
}

func (h *Handler) completeRun(writer http.ResponseWriter, request *http.Request) {
	if !h.authorize(request) {
		http.Error(writer, "invalid coding-agent token", http.StatusUnauthorized)
		return
	}
	var completion codingagent.RunCompletion
	if err := decodeBody(request, &completion); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	session, err := h.store.CompleteCodingAgentRun(request.Context(), request.PathValue("run"), completion)
	if err != nil {
		http.Error(writer, err.Error(), statusFor(err))
		return
	}
	writeJSON(writer, http.StatusOK, session)
}

func (h *Handler) failRun(writer http.ResponseWriter, request *http.Request) {
	if !h.authorize(request) {
		http.Error(writer, "invalid coding-agent token", http.StatusUnauthorized)
		return
	}
	var failure codingagent.RunFailure
	if err := decodeBody(request, &failure); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	session, err := h.store.FailCodingAgentRun(request.Context(), request.PathValue("run"), failure)
	if err != nil {
		http.Error(writer, err.Error(), statusFor(err))
		return
	}
	writeJSON(writer, http.StatusOK, session)
}

func decodeBody(request *http.Request, value any) error {
	if err := json.NewDecoder(io.LimitReader(request.Body, 2<<20)).Decode(value); err != nil {
		return errors.New("invalid request body")
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func statusFor(err error) int {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound
	}
	return http.StatusConflict
}

func (h *Handler) authorize(request *http.Request) bool {
	provided := request.Header.Get("X-AI-Factory-Token")
	return len(provided) == len(h.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(h.token)) == 1
}

func (h *Handler) manifest(writer http.ResponseWriter, request *http.Request) {
	if !h.authorize(request) {
		http.Error(writer, "invalid coding-agent token", http.StatusUnauthorized)
		return
	}
	dispatchID := request.PathValue("dispatch")
	manifest, err := h.store.EnsureCodingTaskManifest(request.Context(), dispatchID, "pi", "")
	if errors.Is(err, store.ErrNotFound) {
		http.Error(writer, "dispatch not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(manifest)
}

func (h *Handler) evidence(writer http.ResponseWriter, request *http.Request) {
	if !h.authorize(request) {
		http.Error(writer, "invalid coding-agent token", http.StatusUnauthorized)
		return
	}
	var evidence codingagent.Evidence
	if err := json.NewDecoder(io.LimitReader(request.Body, 2<<20)).Decode(&evidence); err != nil {
		http.Error(writer, "invalid evidence body", http.StatusBadRequest)
		return
	}
	evidence.DispatchID = request.PathValue("dispatch")
	if err := h.store.SaveCodingAgentEvidence(request.Context(), evidence); err != nil {
		http.Error(writer, err.Error(), http.StatusConflict)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}
