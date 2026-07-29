package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/store"
)

//go:embed assets/*
var assetFiles embed.FS

type Reader interface {
	Dashboard(context.Context, int) (store.DashboardData, error)
}

type Handler struct {
	reader        Reader
	projects      map[int64]domain.ProjectConfig
	gitLabBaseURL string
	logger        *slog.Logger
	assets        http.Handler
}

func New(reader Reader, projects map[int64]domain.ProjectConfig, gitLabAPIURL string, logger *slog.Logger) *Handler {
	root, _ := fs.Sub(assetFiles, "assets")
	return &Handler{
		reader:        reader,
		projects:      projects,
		gitLabBaseURL: strings.TrimSuffix(gitLabAPIURL, "/api/v4"),
		logger:        logger,
		assets:        http.FileServer(http.FS(root)),
	}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /dashboard", func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "/dashboard/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /dashboard/", h.index)
	mux.Handle("GET /dashboard/assets/", http.StripPrefix("/dashboard/assets/", h.assets))
	mux.HandleFunc("GET /api/dashboard", h.data)
}

func (h *Handler) index(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/dashboard/" {
		http.NotFound(writer, request)
		return
	}
	content, err := assetFiles.ReadFile("assets/index.html")
	if err != nil {
		h.logger.Error("dashboard asset unavailable", "error", err)
		http.Error(writer, "dashboard unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(content)
}

func (h *Handler) data(writer http.ResponseWriter, request *http.Request) {
	limit := 100
	if raw := request.URL.Query().Get("limit"); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			limit = value
		}
	}
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	result, err := h.reader.Dashboard(ctx, limit)
	if err != nil {
		h.logger.Error("dashboard query failed", "error", err)
		http.Error(writer, "dashboard data unavailable", http.StatusServiceUnavailable)
		return
	}
	for index := range result.Workflows {
		workflow := &result.Workflows[index]
		project, ok := h.projects[workflow.GitLabProjectID]
		if !ok {
			continue
		}
		workflow.ProjectPath = project.Path
		workflow.IssueURL = h.gitLabBaseURL + "/" + project.Path + "/-/issues/" + strconv.FormatInt(workflow.IssueIID, 10)
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(writer).Encode(result); err != nil {
		h.logger.Warn("dashboard response interrupted", "error", err)
	}
}
