package domain

import (
	"errors"
	"regexp"
	"strings"
)

type ControlAction string

const (
	ControlStartCodex ControlAction = "start-codex"
	ControlResetCodex ControlAction = "reset-codex"
	ControlPause      ControlAction = "pause"
	ControlResume     ControlAction = "resume"
	ControlCancel     ControlAction = "cancel"
)

type ControlCommand struct {
	Action     ControlAction
	WorkItemID string
	WorkflowID string
	ClientID   string
	Reason     string
}

var (
	startCodexPattern = regexp.MustCompile(`^/start-codex task:([0-9a-fA-F-]{36}) client:([A-Za-z0-9._:-]{1,128})$`)
	resetCodexPattern = regexp.MustCompile(`^/reset-codex task:([0-9a-fA-F-]{36})\s+(.+)$`)
	pausePattern      = regexp.MustCompile(`^/pause workflow:([0-9a-fA-F-]{36})\s+(.+)$`)
	resumePattern     = regexp.MustCompile(`^/resume workflow:([0-9a-fA-F-]{36})$`)
	cancelPattern     = regexp.MustCompile(`^/cancel workflow:([0-9a-fA-F-]{36})\s+(.+)$`)
)

// ParseControlCommand accepts only an exact command on the first non-empty line.
// Remaining lines are ignored so quoted email bodies cannot become commands.
func ParseControlCommand(note string) (ControlCommand, error) {
	var line string
	for _, candidate := range strings.Split(strings.ReplaceAll(note, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = strings.TrimSpace(candidate)
			break
		}
	}
	if line == "" {
		return ControlCommand{}, errors.New("empty command")
	}
	if match := startCodexPattern.FindStringSubmatch(line); match != nil {
		return ControlCommand{Action: ControlStartCodex, WorkItemID: strings.ToLower(match[1]), ClientID: match[2]}, nil
	}
	if match := resetCodexPattern.FindStringSubmatch(line); match != nil {
		return ControlCommand{Action: ControlResetCodex, WorkItemID: strings.ToLower(match[1]), Reason: strings.TrimSpace(match[2])}, nil
	}
	if match := pausePattern.FindStringSubmatch(line); match != nil {
		return ControlCommand{Action: ControlPause, WorkflowID: strings.ToLower(match[1]), Reason: strings.TrimSpace(match[2])}, nil
	}
	if match := resumePattern.FindStringSubmatch(line); match != nil {
		return ControlCommand{Action: ControlResume, WorkflowID: strings.ToLower(match[1])}, nil
	}
	if match := cancelPattern.FindStringSubmatch(line); match != nil {
		return ControlCommand{Action: ControlCancel, WorkflowID: strings.ToLower(match[1]), Reason: strings.TrimSpace(match[2])}, nil
	}
	return ControlCommand{}, errors.New("no valid control command found")
}
