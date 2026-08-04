package domain

import (
	"errors"
	"regexp"
	"strings"
)

var gateCommandPattern = regexp.MustCompile(`^/(approve|request-changes|reject)\s+gate:([0-9a-fA-F-]{36})$`)

type GateCommand struct {
	Action   GateAction
	GateID   string
	Feedback string
}

func ParseGateCommand(note string) (GateCommand, error) {
	lines := strings.Split(strings.ReplaceAll(note, "\r\n", "\n"), "\n")
	first := -1
	for index, line := range lines {
		if strings.TrimSpace(line) != "" {
			first = index
			break
		}
	}
	if first < 0 {
		return GateCommand{}, errors.New("no valid gate command found")
	}
	line := strings.TrimSpace(lines[first])
	match := gateCommandPattern.FindStringSubmatch(line)
	if match == nil {
		return GateCommand{}, errors.New("no valid gate command found")
	}
	action := GateAction(match[1])
	gateID := strings.ToLower(match[2])
	feedback := strings.TrimSpace(strings.Join(lines[first+1:], "\n"))
	if action != ActionApprove && feedback == "" {
		return GateCommand{}, errors.New("request-changes and reject require feedback")
	}
	return GateCommand{Action: action, GateID: gateID, Feedback: feedback}, nil
}
