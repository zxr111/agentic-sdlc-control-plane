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
}

func New(token string, repository Repository) *Handler {
	return &Handler{token: token, store: repository}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /coding-agent/dispatches/{dispatch}/manifest", h.manifest)
	mux.HandleFunc("POST /coding-agent/dispatches/{dispatch}/evidence", h.evidence)
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
