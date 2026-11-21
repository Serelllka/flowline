package model

import (
	"errors"
	"math"
	"regexp"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
var nodeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

func ValidID(value string) bool         { return nodeIDPattern.MatchString(value) }
func ValidIdentifier(value string) bool { return identifierPattern.MatchString(value) }
func IsBuiltinToken(value string) bool {
	switch value {
	case "context", "repository", "branch", "commit", "pull_request":
		return true
	default:
		return false
	}
}

// Drafts may have unresolved references and missing executor settings, but their
// IDs, coordinates and structure must be safe to render and store.
func ValidateDraft(p Pipeline) error {
	if !ValidID(p.ID) || strings.TrimSpace(p.Name) == "" {
		return errors.New("invalid pipeline ID or name")
	}
	if !finite(p.Viewport.X) || !finite(p.Viewport.Y) || !finite(p.Viewport.Zoom) {
		return errors.New("invalid viewport")
	}

	nodeIDs := map[string]bool{}
	for _, node := range p.Nodes {
		if !ValidID(node.ID) || nodeIDs[node.ID] {
			return errors.New("invalid or duplicate node ID")
		}
		nodeIDs[node.ID] = true
		if !finite(node.X) || !finite(node.Y) {
			return errors.New("invalid node position")
		}

		ids, names := map[string]bool{}, map[string]bool{}
		for _, output := range node.Outputs {
			if !ValidID(output.ID) || ids[output.ID] {
				return errors.New("invalid or duplicate output ID")
			}
			if !ValidIdentifier(output.Name) || IsBuiltinToken(output.Name) || names[output.Name] {
				return errors.New("invalid, reserved or duplicate output name")
			}
			ids[output.ID], names[output.Name] = true, true
		}
		for alias, source := range node.Inputs {
			if !ValidIdentifier(alias) || IsBuiltinToken(alias) || !ValidID(source.NodeID) || !ValidID(source.OutputID) {
				return errors.New("invalid input binding")
			}
		}
	}

	seenEdges := map[Edge]bool{}
	for _, edge := range p.Edges {
		if !nodeIDs[edge.From] || !nodeIDs[edge.To] || seenEdges[edge] || edge.From == edge.To {
			return errors.New("invalid or duplicate edge")
		}
		seenEdges[edge] = true
	}

	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
