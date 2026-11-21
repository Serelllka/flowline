package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"flowline/internal/cli/process"
	"flowline/internal/model"
	"flowline/internal/repository/git"
)

// Provider is a read-only adapter. Other providers implement PRProvider.
// Authentication remains in gh's existing credential store or its environment.
type Provider struct {
	Command string
}

func New() *Provider { return &Provider{} }

const githubPRFields = "number,title,url,state,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository"

var githubRepoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func githubRepository(remote string) (string, error) {
	var path string
	if strings.HasPrefix(remote, "git@github.com:") {
		path = strings.TrimPrefix(remote, "git@github.com:")
	} else {
		u, err := url.Parse(remote)
		if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || (u.Scheme != "https" && u.Scheme != "ssh") || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("origin не указывает на GitHub: ожидается github.com/owner/repository")
		}
		path = strings.TrimPrefix(u.Path, "/")
	}

	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	if !githubRepoName.MatchString(path) || strings.Contains(path, "..") {
		return "", errors.New("не удалось определить owner/repository из origin")
	}

	return path, nil
}

type githubPR struct {
	Number            int    `json:"number"`
	Title             string `json:"title"`
	URL               string `json:"url"`
	State             string `json:"state"`
	HeadRefName       string `json:"headRefName"`
	HeadRefOID        string `json:"headRefOid"`
	BaseRefName       string `json:"baseRefName"`
	BaseRefOID        string `json:"baseRefOid"`
	IsCrossRepository bool   `json:"isCrossRepository"`
}

func (p githubPR) domain(repository string) model.PullRequest {
	return model.PullRequest{Number: p.Number, Title: p.Title, URL: p.URL, Head: p.HeadRefName, Base: p.BaseRefName, HeadSHA: p.HeadRefOID, BaseSHA: p.BaseRefOID, Repository: repository}
}

func (g *Provider) request(ctx context.Context, repo model.Repository, args []string, result any) error {
	command := g.Command
	if command == "" {
		command = "gh"
	}
	if _, err := exec.LookPath(command); err != nil {
		return errors.New("GitHub CLI (gh) не найден. Установите gh и выполните gh auth login")
	}

	var diagnostic strings.Builder
	out, _, err := process.RunProcess(ctx, append([]string{command}, args...), repo.Path, "", func(chunk string) {
		if strings.HasPrefix(chunk, "[stderr]") && diagnostic.Len() < 4096 {
			diagnostic.WriteString(chunk)
		}
	})
	if err != nil {
		return fmt.Errorf("доступ к GitHub: %w. %s", err, process.Redact(strings.TrimSpace(diagnostic.String())))
	}

	if err := json.Unmarshal([]byte(out), result); err != nil {
		return fmt.Errorf("GitHub вернул неверный JSON: %w", err)
	}

	return nil
}

func (g *Provider) List(ctx context.Context, repo model.Repository) ([]model.PullRequest, error) {
	name, err := githubRepository(repo.Remote)
	if err != nil {
		return nil, err
	}

	var rows []githubPR
	if err := g.request(ctx, repo, []string{"pr", "list", "--repo", "github.com/" + name, "--state", "open", "--limit", "100", "--json", githubPRFields}, &rows); err != nil {
		return nil, err
	}

	result := make([]model.PullRequest, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.domain(name))
	}

	return result, nil
}

func (g *Provider) Validate(ctx context.Context, repo model.Repository, branch, sha string, number int) (*model.PullRequest, error) {
	if number <= 0 {
		return nil, errors.New("неверный номер PR")
	}

	name, err := githubRepository(repo.Remote)
	if err != nil {
		return nil, err
	}

	current, err := git.InspectRepository(repo.Path)
	if err != nil {
		return nil, err
	}
	actual, err := githubRepository(current.Remote)
	if err != nil || !strings.EqualFold(actual, name) {
		return nil, errors.New("origin изменился после добавления репозитория; добавьте путь ещё раз для обновления контекста")
	}

	var row githubPR
	if err := g.request(ctx, repo, []string{"pr", "view", strconv.Itoa(number), "--repo", "github.com/" + name, "--json", githubPRFields}, &row); err != nil {
		return nil, err
	}

	if row.Number != number || row.State != "OPEN" {
		return nil, errors.New("PR не найден или уже закрыт")
	}
	if row.IsCrossRepository {
		return nil, errors.New("PR из fork пока не поддерживается: выберите ветку PR в репозитории-источнике или локальный запуск без PR")
	}
	if row.HeadRefName != branch || row.HeadRefOID != sha {
		return nil, fmt.Errorf("PR #%d ожидает ветку %s на commit %s; выбрана %s на %s. Синхронизируйте ветку", number, row.HeadRefName, row.HeadRefOID, branch, sha)
	}
	if row.BaseRefName == "" || row.BaseRefOID == "" {
		return nil, errors.New("GitHub не вернул целевую ветку PR")
	}

	result := row.domain(name)

	return &result, nil
}
