package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
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
	secret         string
	callbackSecret string
	projects       map[int64]domain.ProjectConfig
	store          EventWriter
	logger         *slog.Logger
}

func New(secret string, projects map[int64]domain.ProjectConfig, store EventWriter, logger *slog.Logger) *Handler {
	return NewWithCallbackSecret(secret, secret, projects, store, logger)
}

func NewWithCallbackSecret(secret, callbackSecret string, projects map[int64]domain.ProjectConfig, store EventWriter, logger *slog.Logger) *Handler {
	return &Handler{secret: secret, callbackSecret: callbackSecret, projects: projects, store: store, logger: logger}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks/gitlab", h.gitLab)
	mux.HandleFunc("POST /callbacks/jenkins", h.externalCallback("jenkins"))
	mux.HandleFunc("POST /callbacks/monitoring", h.externalCallback("monitoring"))
	mux.HandleFunc("POST /callbacks/quality", h.externalCallback("quality"))
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
		State        string `json:"state"`
		Status       string `json:"status"`
		Ref          string `json:"ref"`
		SHA          string `json:"sha"`
		SourceBranch string `json:"source_branch"`
		TargetBranch string `json:"target_branch"`
		LastCommit   struct {
			ID string `json:"id"`
		} `json:"last_commit"`
		URL    string `json:"url"`
		Labels []struct {
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
	ProjectID      int64              `json:"project_id"`
	IssueIID       int64              `json:"issue_iid"`
	NoteID         int64              `json:"note_id"`
	UserID         int64              `json:"user_id"`
	Username       string             `json:"username"`
	Command        domain.GateCommand `json:"command"`
	EventID        string             `json:"event_id"`
	EmailRelayHash string             `json:"email_relay_hash,omitempty"`
}

type ControlNote struct {
	ProjectID      int64                 `json:"project_id"`
	IssueIID       int64                 `json:"issue_iid"`
	NoteID         int64                 `json:"note_id"`
	UserID         int64                 `json:"user_id"`
	Username       string                `json:"username"`
	Command        domain.ControlCommand `json:"command"`
	EventID        string                `json:"event_id"`
	EmailRelayHash string                `json:"email_relay_hash,omitempty"`
}

var emailRelayMarker = regexp.MustCompile(`<!-- ai-factory:email-relay:([a-f0-9]{64}) -->`)

type LifecycleEvent struct {
	ProjectID       int64  `json:"project_id"`
	ObjectKind      string `json:"object_kind"`
	ObjectID        int64  `json:"object_id"`
	IssueIID        int64  `json:"issue_iid"`
	MergeRequestIID int64  `json:"merge_request_iid"`
	Action          string `json:"action"`
	State           string `json:"state"`
	Status          string `json:"status"`
	SourceBranch    string `json:"source_branch"`
	TargetBranch    string `json:"target_branch"`
	SHA             string `json:"sha"`
	URL             string `json:"url"`
	UserID          int64  `json:"user_id"`
	Username        string `json:"username"`
	EventID         string `json:"event_id"`
}

type ExternalCallback struct {
	Source                      string          `json:"source"`
	ExternalID                  string          `json:"external_id"`
	ProjectID                   int64           `json:"project_id"`
	WorkflowID                  string          `json:"workflow_id"`
	WorkItemID                  string          `json:"work_item_id"`
	IssueIID                    int64           `json:"issue_iid"`
	Severity                    string          `json:"severity"`
	Status                      string          `json:"status"`
	Title                       string          `json:"title"`
	Environment                 string          `json:"environment"`
	CommitSHA                   string          `json:"commit_sha"`
	RequiresProductionMigration bool            `json:"requires_production_migration"`
	MigrationPlan               string          `json:"migration_plan"`
	RollbackPlan                string          `json:"rollback_plan"`
	ChangeWindow                string          `json:"change_window"`
	Payload                     json.RawMessage `json:"payload"`
}

func (h *Handler) gitLab(writer http.ResponseWriter, request *http.Request) {
	if !secureEqual(request.Header.Get("X-Gitlab-Token"), h.secret) {
		http.Error(writer, "invalid webhook token", http.StatusUnauthorized)
		return
	}
	eventName := request.Header.Get("X-Gitlab-Event")
	enabledEvents := map[string]bool{
		"Issue Hook": true, "Note Hook": true, "Merge Request Hook": true,
		"Pipeline Hook": true, "Job Hook": true, "Deployment Hook": true, "Push Hook": true,
	}
	if !enabledEvents[eventName] {
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
		if envelope.ObjectAttributes.NoteableType != "Issue" && envelope.ObjectAttributes.NoteableType != "MergeRequest" {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		command, parseErr := domain.ParseGateCommand(envelope.ObjectAttributes.Note)
		issueIID := envelope.Issue.IID
		if issueIID == 0 {
			issueIID = envelope.ObjectAttributes.NoteableIID
		}
		if parseErr == nil {
			relayHash := ""
			if match := emailRelayMarker.FindStringSubmatch(envelope.ObjectAttributes.Note); match != nil {
				relayHash = match[1]
			}
			event := GateNote{
				ProjectID: envelope.Project.ID, IssueIID: issueIID, NoteID: envelope.ObjectAttributes.ID,
				UserID: envelope.User.ID, Username: envelope.User.Username, Command: command, EventID: eventID,
				EmailRelayHash: relayHash,
			}
			err = h.store.EnqueueEvent(request.Context(), "gitlab:"+eventID, "gitlab.gate.command", event, time.Now().UTC())
			break
		}
		control, controlErr := domain.ParseControlCommand(envelope.ObjectAttributes.Note)
		if controlErr != nil {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		event := ControlNote{
			ProjectID: envelope.Project.ID, IssueIID: issueIID, NoteID: envelope.ObjectAttributes.ID,
			UserID: envelope.User.ID, Username: envelope.User.Username, Command: control, EventID: eventID,
		}
		if match := emailRelayMarker.FindStringSubmatch(envelope.ObjectAttributes.Note); match != nil {
			event.EmailRelayHash = match[1]
		}
		err = h.store.EnqueueEvent(request.Context(), "gitlab:"+eventID, "gitlab.control.command", event, time.Now().UTC())
	default:
		lifecycle := LifecycleEvent{
			ProjectID: envelope.Project.ID, ObjectKind: envelope.ObjectKind,
			ObjectID: envelope.ObjectAttributes.ID, IssueIID: envelope.Issue.IID,
			MergeRequestIID: envelope.ObjectAttributes.IID, Action: envelope.ObjectAttributes.Action,
			State: envelope.ObjectAttributes.State, Status: envelope.ObjectAttributes.Status,
			SourceBranch: envelope.ObjectAttributes.SourceBranch, TargetBranch: envelope.ObjectAttributes.TargetBranch,
			SHA: envelope.ObjectAttributes.LastCommit.ID, URL: envelope.ObjectAttributes.URL,
			UserID: envelope.User.ID, Username: envelope.User.Username, EventID: eventID,
		}
		if lifecycle.SourceBranch == "" {
			lifecycle.SourceBranch = envelope.ObjectAttributes.Ref
		}
		if lifecycle.SHA == "" {
			lifecycle.SHA = envelope.ObjectAttributes.SHA
		}
		err = h.store.EnqueueEvent(request.Context(), "gitlab:"+eventID, "gitlab.lifecycle", lifecycle, time.Now().UTC())
	}
	if err != nil {
		h.logger.Error("webhook enqueue failed", "event_id", eventID, "error", err)
		http.Error(writer, "event could not be persisted", http.StatusInternalServerError)
		return
	}
	writer.WriteHeader(http.StatusAccepted)
}

func (h *Handler) externalCallback(source string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !secureEqual(request.Header.Get("X-AI-Factory-Token"), h.callbackSecret) {
			http.Error(writer, "invalid callback token", http.StatusUnauthorized)
			return
		}
		var callback ExternalCallback
		if err := json.NewDecoder(io.LimitReader(request.Body, 2<<20)).Decode(&callback); err != nil {
			http.Error(writer, "invalid callback body", http.StatusBadRequest)
			return
		}
		callback.Source = source
		if callback.ExternalID == "" {
			http.Error(writer, "external_id is required", http.StatusBadRequest)
			return
		}
		if callback.ProjectID != 0 {
			if _, ok := h.projects[callback.ProjectID]; !ok {
				http.Error(writer, "project is not configured", http.StatusForbidden)
				return
			}
		}
		eventType := "external.delivery"
		if source == "monitoring" {
			eventType = "external.incident"
		}
		if err := h.store.EnqueueEvent(request.Context(), source+":"+callback.ExternalID+":"+callback.Status,
			eventType, callback, time.Now().UTC()); err != nil {
			http.Error(writer, "event could not be persisted", http.StatusInternalServerError)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	}
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
