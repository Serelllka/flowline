package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"flowline/internal/model"
	"flowline/internal/service/graph"
)

func (s *Server) savePipeline(w http.ResponseWriter, r *http.Request) {
	var p model.Pipeline
	if !readJSON(w, r, &p) {
		return
	}

	if p.ID != r.PathValue("id") || p.ID == "" || strings.TrimSpace(p.Name) == "" || len(p.Name) > 200 || len(p.Nodes) > 100 || len(p.Edges) > 1000 {
		fail(w, 400, errors.New("неверные ID, название или размер пайплайна"))
		return
	}

	p.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.store.SavePipeline(p); err != nil {
		fail(w, 400, err)
		return
	}

	writeJSON(w, 200, p)
}

func (s *Server) validatePipeline(w http.ResponseWriter, r *http.Request) {
	var p model.Pipeline
	if !readJSON(w, r, &p) {
		return
	}

	issues := graph.ValidateGraph(p)
	if issues == nil {
		issues = []string{}
	}

	writeJSON(w, 200, map[string]any{"errors": issues})
}

func (s *Server) renameOutput(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Pipeline model.Pipeline `json:"pipeline"`
		NodeID   string         `json:"nodeId"`
		OutputID string         `json:"outputId"`
		Name     string         `json:"name"`
	}
	if !readJSON(w, r, &request) {
		return
	}

	if err := graph.RenameOutput(&request.Pipeline, request.NodeID, request.OutputID, request.Name); err != nil {
		fail(w, 400, err)
		return
	}

	writeJSON(w, 200, request.Pipeline)
}
