package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 90 * time.Second},
	}
}

type Issue struct {
	ID          int64    `json:"id"`
	IID         int64    `json:"iid"`
	ProjectID   int64    `json:"project_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	WebURL      string   `json:"web_url"`
	Labels      []string `json:"labels"`
	Author      User     `json:"author"`
}

func (i Issue) HasLabel(label string) bool {
	for _, value := range i.Labels {
		if value == label {
			return true
		}
	}
	return false
}

type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	State    string `json:"state"`
}

type Note struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	System    bool   `json:"system"`
	CreatedAt string `json:"created_at"`
	Author    User   `json:"author"`
}

func (c *Client) GetIssue(ctx context.Context, projectID, issueIID int64) (Issue, error) {
	var result Issue
	err := c.json(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/issues/%d", projectID, issueIID), nil, &result)
	return result, err
}

func (c *Client) ListNotes(ctx context.Context, projectID, issueIID int64) ([]Note, error) {
	var result []Note
	path := fmt.Sprintf("/projects/%d/issues/%d/notes?sort=desc&order_by=created_at&per_page=100", projectID, issueIID)
	err := c.json(ctx, http.MethodGet, path, nil, &result)
	return result, err
}

func (c *Client) UpsertNote(ctx context.Context, projectID, issueIID int64, marker, body string) (int64, error) {
	notes, err := c.ListNotes(ctx, projectID, issueIID)
	if err != nil {
		return 0, err
	}
	for _, note := range notes {
		if strings.Contains(note.Body, marker) {
			var updated Note
			err := c.json(ctx, http.MethodPut,
				fmt.Sprintf("/projects/%d/issues/%d/notes/%d", projectID, issueIID, note.ID),
				map[string]string{"body": marker + "\n" + body}, &updated)
			return updated.ID, err
		}
	}
	var created Note
	err = c.json(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/issues/%d/notes", projectID, issueIID),
		map[string]string{"body": marker + "\n" + body}, &created)
	return created.ID, err
}

type CreateIssueInput struct {
	ProjectID   int64
	Title       string
	Description string
	Labels      []string
	AssigneeID  int64
	Marker      string
}

func (c *Client) CreateIssueIdempotent(ctx context.Context, input CreateIssueInput) (Issue, error) {
	existing, err := c.findIssueByMarker(ctx, input.ProjectID, input.Title, input.Marker)
	if err != nil {
		return Issue{}, err
	}
	if existing.ID != 0 {
		return existing, nil
	}
	payload := map[string]any{
		"title":       input.Title,
		"description": input.Marker + "\n" + input.Description,
		"labels":      strings.Join(input.Labels, ","),
	}
	if input.AssigneeID > 0 {
		payload["assignee_id"] = input.AssigneeID
	}
	var created Issue
	err = c.json(ctx, http.MethodPost, fmt.Sprintf("/projects/%d/issues", input.ProjectID), payload, &created)
	if err != nil {
		// A timeout can happen after GitLab committed the issue. Re-read before returning.
		if recovered, findErr := c.findIssueByMarker(ctx, input.ProjectID, input.Title, input.Marker); findErr == nil && recovered.ID != 0 {
			return recovered, nil
		}
	}
	return created, err
}

func (c *Client) findIssueByMarker(ctx context.Context, projectID int64, title, marker string) (Issue, error) {
	query := url.Values{}
	query.Set("scope", "all")
	query.Set("search", title)
	query.Set("in", "title")
	query.Set("per_page", "100")
	var issues []Issue
	if err := c.json(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/issues?%s", projectID, query.Encode()), nil, &issues); err != nil {
		return Issue{}, err
	}
	for _, issue := range issues {
		if strings.Contains(issue.Description, marker) {
			return issue, nil
		}
	}
	return Issue{}, nil
}

func (c *Client) Upload(ctx context.Context, projectID int64, filename, mediaType string, content []byte) (string, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", "", err
	}
	if _, err := part.Write(content); err != nil {
		return "", "", err
	}
	if err := writer.Close(); err != nil {
		return "", "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/projects/%d/uploads", c.baseURL, projectID), &body)
	if err != nil {
		return "", "", err
	}
	request.Header.Set("PRIVATE-TOKEN", c.token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return "", "", fmt.Errorf("gitlab upload failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", responseError("gitlab upload", response)
	}
	var result struct {
		URL      string `json:"url"`
		Markdown string `json:"markdown"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil {
		return "", "", err
	}
	return result.URL, result.Markdown, nil
}

func (c *Client) IsActiveProjectMember(ctx context.Context, projectID, userID int64) (bool, error) {
	var user User
	err := c.json(ctx, http.MethodGet, fmt.Sprintf("/projects/%d/members/all/%d", projectID, userID), nil, &user)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return user.ID == userID && user.State == "active", nil
}

func (c *Client) json(ctx context.Context, method, path string, body any, result any) error {
	var content io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		content = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, content)
	if err != nil {
		return err
	}
	request.Header.Set("PRIVATE-TOKEN", c.token)
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("gitlab %s failed: %w", method, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError("gitlab "+method, response)
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 10<<20)).Decode(result)
}

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("gitlab API returned HTTP %d: %s", e.StatusCode, e.Message)
}

func responseError(operation string, response *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	message := strings.TrimSpace(string(raw))
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return &APIError{StatusCode: response.StatusCode, Message: operation + ": " + message}
}

func ExternalID(id int64) string { return strconv.FormatInt(id, 10) }
