package domain

import (
	"errors"
	"regexp"
	"strings"
)

var gateCommandPattern = regexp.MustCompile(`(?m)^\s*/(approve|request-changes|reject)\s+gate:([0-9a-fA-F-]{36})\s*$`)

type GateCommand struct {
	Action   GateAction
	GateID   string
	Feedback string
}

func ParseGateCommand(note string) (GateCommand, error) {
	match := gateCommandPattern.FindStringSubmatchIndex(note)
	if match == nil {
		return GateCommand{}, errors.New("no valid gate command found")
	}
	action := GateAction(note[match[2]:match[3]])
	gateID := strings.ToLower(note[match[4]:match[5]])
	feedback := strings.TrimSpace(note[match[1]:])
	if action != ActionApprove && feedback == "" {
		return GateCommand{}, errors.New("request-changes and reject require feedback")
	}
	return GateCommand{Action: action, GateID: gateID, Feedback: feedback}, nil
}
