package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
)

type Config struct {
	HTTPAddr            string
	MySQLDSN            string
	GitLabAPIURL        string
	GitLabToken         string
	GitLabWebhookSecret string
	ConfluenceBaseURL   string
	ConfluenceEmail     string
	ConfluenceToken     string
	OpenAIAPIURL        string
	OpenAIAPIKey        string
	OpenAIModel         string
	WorkerID            string
	WorkerPollInterval  time.Duration
	LeaseDuration       time.Duration
	ReconcileInterval   time.Duration
	MaxAttempts         int
	Projects            map[int64]domain.ProjectConfig
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:            env("HTTP_ADDR", ":8080"),
		MySQLDSN:            os.Getenv("MYSQL_DSN"),
		GitLabAPIURL:        strings.TrimRight(env("GITLAB_API_URL", "https://git.kuainiujinke.com/api/v4"), "/"),
		GitLabToken:         os.Getenv("GITLAB_API_TOKEN"),
		ConfluenceBaseURL:   strings.TrimRight(env("CONFLUENCE_BASE_URL", "https://kylith.atlassian.net"), "/"),
		ConfluenceEmail:     os.Getenv("CONFLUENCE_EMAIL"),
		ConfluenceToken:     os.Getenv("CONFLUENCE_API_TOKEN"),
		OpenAIAPIURL:        strings.TrimRight(env("OPENAI_API_URL", "https://api.openai.com/v1"), "/"),
		OpenAIAPIKey:        os.Getenv("OPENAI_API_KEY"),
		OpenAIModel:         env("OPENAI_MODEL", "gpt-5.6-terra"),
		GitLabWebhookSecret: os.Getenv("GITLAB_WEBHOOK_SECRET"),
		WorkerID:            env("WORKER_ID", hostname()),
		WorkerPollInterval:  durationEnv("WORKER_POLL_INTERVAL", time.Second),
		LeaseDuration:       durationEnv("WORKER_LEASE_DURATION", 2*time.Minute),
		ReconcileInterval:   durationEnv("RECONCILE_INTERVAL", 10*time.Minute),
		MaxAttempts:         intEnv("MAX_ATTEMPTS", 8),
	}

	projects, err := loadProjects()
	if err != nil {
		return Config{}, err
	}
	cfg.Projects = projects
	return cfg, cfg.Validate()
}

func (c Config) Validate() error {
	required := map[string]string{
		"MYSQL_DSN":             c.MySQLDSN,
		"GITLAB_API_TOKEN":      c.GitLabToken,
		"GITLAB_WEBHOOK_SECRET": c.GitLabWebhookSecret,
		"CONFLUENCE_EMAIL":      c.ConfluenceEmail,
		"CONFLUENCE_API_TOKEN":  c.ConfluenceToken,
		"OPENAI_API_KEY":        c.OpenAIAPIKey,
	}
	var missing []string
	for name, value := range required {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if len(c.Projects) == 0 {
		return errors.New("no projects configured")
	}
	return nil
}

func (c Config) Project(projectID int64) (domain.ProjectConfig, bool) {
	project, ok := c.Projects[projectID]
	return project, ok
}

func loadProjects() (map[int64]domain.ProjectConfig, error) {
	raw := os.Getenv("FACTORY_PROJECTS_JSON")
	if file := os.Getenv("FACTORY_PROJECTS_FILE"); raw == "" && file != "" {
		value, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read project configuration: %w", err)
		}
		raw = string(value)
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("FACTORY_PROJECTS_JSON or FACTORY_PROJECTS_FILE is required")
	}
	var values []domain.ProjectConfig
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("parse project configuration: %w", err)
	}
	result := make(map[int64]domain.ProjectConfig, len(values))
	for _, project := range values {
		if project.EnabledLabel == "" {
			project.EnabledLabel = "automation::enabled"
		}
		if err := project.Validate(); err != nil {
			return nil, err
		}
		if _, exists := result[project.GitLabProjectID]; exists {
			return nil, fmt.Errorf("duplicate project id %d", project.GitLabProjectID)
		}
		result[project.GitLabProjectID] = project
	}
	return result, nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func intEnv(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "factory-worker"
	}
	return name
}
