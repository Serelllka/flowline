package httpapi

import (
	"context"
	"net/http"
	"time"

	"flowline/internal/cli/process"
	"flowline/internal/model"
	"flowline/internal/repository/git"
)

func (s *Server) addRepository(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Path string `json:"path"`
	}
	if !readJSON(w, r, &request) {
		return
	}

	repo, err := git.InspectRepository(request.Path)
	if err != nil {
		fail(w, 400, err)
		return
	}

	existing, err := s.store.Repositories()
	if err != nil {
		fail(w, 500, err)
		return
	}

	for _, item := range existing {
		if item.Path == repo.Path {
			repo.ID = item.ID
			if err := s.store.SaveRepository(repo); err != nil {
				fail(w, 500, err)
				return
			}
			writeJSON(w, 200, repo)
			return
		}
	}

	repo.ID = model.NewID()
	if err := s.store.SaveRepository(repo); err != nil {
		fail(w, 500, err)
		return
	}

	writeJSON(w, http.StatusCreated, repo)
}

func (s *Server) repositoryContext(w http.ResponseWriter, r *http.Request) {
	repo, err := s.store.Repository(r.PathValue("id"))
	if err != nil {
		fail(w, 404, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	branches, err := git.ListBranches(ctx, repo)
	if err != nil {
		fail(w, 400, err)
		return
	}

	prs := []model.PullRequest{}
	prError := "Git-провайдер не настроен. Доступен запуск локальной ветки без PR."
	if s.provider != nil {
		prs, err = s.provider.List(ctx, repo)
		prError = ""
		if err != nil {
			prs, prError = []model.PullRequest{}, process.Redact(err.Error())
		}
	}

	writeJSON(w, 200, map[string]any{"branches": branches, "pullRequests": prs, "prError": prError})
}
