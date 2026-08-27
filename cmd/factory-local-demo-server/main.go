// Command factory-local-demo-server supplies deterministic local-only model
// and GitLab responses for exercising the control plane without external data.
package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/modelmock"
)

type fixture struct {
	notes  atomic.Int64
	issues atomic.Int64
}

func main() {
	model := new(modelmock.Server).Routes()
	fixture := new(fixture)
	fixture.notes.Store(100)
	fixture.issues.Store(10)
	mux := http.NewServeMux()
	mux.Handle("/v1/", http.StripPrefix("/v1", model))
	mux.HandleFunc("/api/v4/", fixture.gitlab)
	mux.HandleFunc("/wiki/api/v2/pages/", fixture.confluence)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"status": "ok", "mode": "local-demo"})
	})
	server := &http.Server{Addr: "127.0.0.1:19090", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	slog.Info("local integration fixture listening", "address", server.Addr, "production_evidence", false)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("local integration fixture stopped", "error", err)
		os.Exit(1)
	}
}

func (f *fixture) confluence(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "/attachments") {
		writeJSON(w, map[string]any{"results": []any{}, "_links": map[string]any{"next": ""}})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	pageID := parts[len(parts)-1]
	writeJSON(w, map[string]any{
		"id": pageID, "title": "Hello World API 本地演示需求", "spaceId": "DEMO",
		"version": map[string]any{"number": 1, "createdAt": time.Now().UTC().Format(time.RFC3339)},
		"body":    map[string]any{"storage": map[string]any{"value": "<h1>Hello World API</h1><p>新增 GET /hello 接口，返回 HTTP 200 和 JSON：{&quot;message&quot;:&quot;Hello, World!&quot;}。其他 HTTP 方法返回 405。必须包含自动化测试。</p>"}},
		"_links":  map[string]any{"webui": "/wiki/spaces/DEMO/pages/" + pageID + "/Hello-World"},
	})
}

func (f *fixture) gitlab(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v4")
	if strings.Contains(path, "/members/all/") {
		parts := strings.Split(path, "/")
		id, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		writeJSON(w, map[string]any{"id": id, "username": "local-reviewer", "state": "active"})
		return
	}
	if strings.HasSuffix(path, "/notes") {
		if r.Method == http.MethodGet {
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, map[string]any{"id": f.notes.Add(1), "body": "local demo note", "author": map[string]any{"id": 995, "username": "local-reviewer", "state": "active"}})
		return
	}
	if strings.Contains(path, "/notes/") {
		writeJSON(w, map[string]any{"id": f.notes.Add(1), "body": "local demo note"})
		return
	}
	if strings.HasSuffix(path, "/issues") {
		if r.Method == http.MethodGet {
			writeJSON(w, []any{})
			return
		}
		iid := f.issues.Add(1)
		writeJSON(w, map[string]any{"id": iid, "iid": iid, "project_id": 3533, "title": "Local demo work item", "state": "opened", "web_url": "http://127.0.0.1:19090/demo/issues/" + strconv.FormatInt(iid, 10)})
		return
	}
	if strings.Contains(path, "/issues/") {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		iid, _ := strconv.ParseInt(parts[len(parts)-1], 10, 64)
		writeJSON(w, map[string]any{"id": iid, "iid": iid, "project_id": 3533, "title": "Hello World API 完整流程演示", "description": "需求来源：https://local.invalid/wiki/spaces/DEMO/pages/2/Hello-World", "state": "opened", "labels": []string{"automation::enabled"}, "author": map[string]any{"id": 995, "username": "local-reviewer", "state": "active"}})
		return
	}
	if strings.Contains(path, "/repository/branches") {
		writeJSON(w, map[string]any{"name": "local-demo"})
		return
	}
	if strings.Contains(path, "/merge_requests") {
		if r.Method == http.MethodGet && !strings.Contains(path, "/merge_requests/") {
			writeJSON(w, []any{})
			return
		}
		writeJSON(w, map[string]any{"id": 1, "iid": 1, "project_id": 3533, "title": "Local demo MR", "state": "opened", "source_branch": "ai/2/11-hello-world", "target_branch": "master", "sha": strings.Repeat("a", 40), "web_url": "http://127.0.0.1:19090/demo/merge_requests/1", "detailed_merge_status": "mergeable"})
		return
	}
	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Factory-Demo-Only", "true")
	_ = json.NewEncoder(w).Encode(value)
}
