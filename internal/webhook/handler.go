package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

type EventWriter interface {
	EnqueueEvent(context.Context, string, string, any, time.Time) error
	Ping(context.Context) error
}

type Handler struct {
	secret   string
	projects map[int64]domain.ProjectConfig
	store    EventWriter
	logger   *slog.Logger
}

func New(secret string, projects map[int64]domain.ProjectConfig, store EventWriter, logger *slog.Logger) *Handler {
	return &Handler{secret: secret, projects: projects, store: store, logger: logger}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/gitlab", h.gitLab)
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if err := h.store.Ping(ctx); err != nil {
			http.Error(writer, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	})
	return mux
}

type gitLabEnvelope struct {
	ObjectKind string `json:"object_kind"`
	EventType  string `json:"event_type"`
	Project    struct {
		ID int64 `json:"id"`
	} `json:"project"`
	User struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	Issue struct {
		IID int64 `json:"iid"`
	} `json:"issue"`
	ObjectAttributes struct {
		ID           int64  `json:"id"`
		IID          int64  `json:"iid"`
		NoteableIID  int64  `json:"noteable_iid"`
		Title        string `json:"title"`
		Description  string `json:"description"`
		Action       string `json:"action"`
		Note         string `json:"note"`
		NoteableType string `json:"noteable_type"`
		Labels       []struct {
			Title string `json:"title"`
		} `json:"labels"`
	} `json:"object_attributes"`
	Labels []struct {
		Title string `json:"title"`
	} `json:"labels"`
}

type IssueChanged struct {
	ProjectID   int64  `json:"project_id"`
	IssueIID    int64  `json:"issue_iid"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Action      string `json:"action"`
	EventID     string `json:"event_id"`
}

type GateNote struct {
	ProjectID int64              `json:"project_id"`
	IssueIID  int64              `json:"issue_iid"`
	NoteID    int64              `json:"note_id"`
	UserID    int64              `json:"user_id"`
	Username  string             `json:"username"`
	Command   domain.GateCommand `json:"command"`
	EventID   string             `json:"event_id"`
}

func (h *Handler) gitLab(writer http.ResponseWriter, request *http.Request) {
	if !secureEqual(request.Header.Get("X-Gitlab-Token"), h.secret) {
		http.Error(writer, "invalid webhook token", http.StatusUnauthorized)
		return
	}
	eventName := request.Header.Get("X-Gitlab-Event")
	if eventName != "Issue Hook" && eventName != "Note Hook" {
		http.Error(writer, "event type is not enabled", http.StatusBadRequest)
		return
	}
	var envelope gitLabEnvelope
	decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
	if err := decoder.Decode(&envelope); err != nil {
		http.Error(writer, "invalid webhook body", http.StatusBadRequest)
		return
	}
	project, ok := h.projects[envelope.Project.ID]
	if !ok {
		http.Error(writer, "project is not configured", http.StatusForbidden)
		return
	}
	eventID := request.Header.Get("X-Gitlab-Event-UUID")
	if eventID == "" {
		eventID = fallbackEventID(envelope)
	}

	var err error
	switch eventName {
	case "Issue Hook":
		if !hasLabel(envelope, project.EnabledLabel) {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		event := IssueChanged{
			ProjectID: envelope.Project.ID, IssueIID: envelope.ObjectAttributes.IID,
			Title: envelope.ObjectAttributes.Title, Description: envelope.ObjectAttributes.Description,
			Action: envelope.ObjectAttributes.Action, EventID: eventID,
		}
		err = h.store.EnqueueEvent(request.Context(), "gitlab:"+eventID, "gitlab.issue.changed", event, time.Now().UTC())
	case "Note Hook":
		if envelope.ObjectAttributes.NoteableType != "Issue" {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		command, parseErr := domain.ParseGateCommand(envelope.ObjectAttributes.Note)
		if parseErr != nil {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		issueIID := envelope.Issue.IID
		if issueIID == 0 {
			issueIID = envelope.ObjectAttributes.NoteableIID
		}
		event := GateNote{
			ProjectID: envelope.Project.ID, IssueIID: issueIID, NoteID: envelope.ObjectAttributes.ID,
			UserID: envelope.User.ID, Username: envelope.User.Username, Command: command, EventID: eventID,
		}
		err = h.store.EnqueueEvent(request.Context(), "gitlab:"+eventID, "gitlab.gate.command", event, time.Now().UTC())
	}
	if err != nil {
		h.logger.Error("webhook enqueue failed", "event_id", eventID, "error", err)
		http.Error(writer, "event could not be persisted", http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}

func hasLabel(envelope gitLabEnvelope, expected string) bool {
	for _, label := range envelope.Labels {
		if label.Title == expected {
			return true
		}
	}
	for _, label := range envelope.ObjectAttributes.Labels {
		if label.Title == expected {
			return true
		}
	}
	return false
}

func fallbackEventID(envelope gitLabEnvelope) string {
	issueIID := envelope.ObjectAttributes.IID
	if issueIID == 0 {
		issueIID = envelope.Issue.IID
	}
	return strings.Join([]string{
		envelope.ObjectKind,
		strconv.FormatInt(envelope.Project.ID, 10),
		strconv.FormatInt(issueIID, 10),
		strconv.FormatInt(envelope.ObjectAttributes.ID, 10),
		envelope.ObjectAttributes.Action,
	}, ":")
}

func secureEqual(provided, expected string) bool {
	if provided == "" || expected == "" || len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func RequestID(request *http.Request) string {
	if value := request.Header.Get("X-Request-ID"); value != "" {
		return value
	}
	return fmt.Sprintf("http-%d", time.Now().UnixNano())
}
