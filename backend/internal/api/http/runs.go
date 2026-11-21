package httpapi

import (
	"net/http"
)

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PipelineID   string `json:"pipelineId"`
		RepositoryID string `json:"repositoryId"`
		Branch       string `json:"branch"`
		PRNumber     int    `json:"prNumber"`
	}
	if !readJSON(w, r, &request) {
		return
	}

	run, err := s.engine.Start(r.Context(), request.PipelineID, request.RepositoryID, request.Branch, request.PRNumber)
	if err != nil {
		fail(w, 400, err)
		return
	}

	writeJSON(w, http.StatusCreated, run)
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	runs, err := s.engine.Runs()
	if err != nil {
		fail(w, 500, err)
		return
	}

	writeJSON(w, 200, runs)
}

func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.Run(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}

	writeJSON(w, 200, run)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.Cancel(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}

	writeJSON(w, 200, run)
}
