package github

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"flowline/internal/model"
	"flowline/internal/repository/git"
	"flowline/internal/testutil"
)

func githubFixture(t *testing.T) (*Provider, model.Repository, githubPR, string, string) {
	t.Helper()
	repo, sha := testutil.Repository(t)
	testutil.Git(t, repo.Path, "remote", "add", "origin", "git@github.com:owner/repo.git")
	var err error
	repo, err = git.InspectRepository(repo.Path)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	command := filepath.Join(dir, "gh-fixture")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$@" > "$FLOWLINE_GH_ARGUMENTS"
if [ "${FLOWLINE_GH_MODE:-}" = noauth ]; then
  printf '%s\n' "authentication failed ${FLOWLINE_TEST_GITHUB_TOKEN:-}" >&2
  exit 4
fi
case "$1 $2" in
  'pr list') cat "$FLOWLINE_GH_LIST" ;;
  'pr view') cat "$FLOWLINE_GH_VIEW" ;;
  *) printf 'unexpected gh command\n' >&2; exit 9 ;;
esac
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	listPath, viewPath, argsPath := filepath.Join(dir, "list.json"), filepath.Join(dir, "view.json"), filepath.Join(dir, "args.txt")
	t.Setenv("FLOWLINE_GH_LIST", listPath)
	t.Setenv("FLOWLINE_GH_VIEW", viewPath)
	t.Setenv("FLOWLINE_GH_ARGUMENTS", argsPath)
	t.Setenv("FLOWLINE_GH_MODE", "")
	t.Setenv("GH_HOST", "enterprise.example")
	pr := githubPR{Number: 42, Title: "Actual PR fixture", URL: "https://github.com/owner/repo/pull/42", State: "OPEN", HeadRefName: "main", HeadRefOID: sha, BaseRefName: "release", BaseRefOID: strings.Repeat("a", 40)}
	writeGitHubFixture(t, listPath, []githubPR{pr})
	writeGitHubFixture(t, viewPath, pr)

	return &Provider{Command: command}, repo, pr, viewPath, argsPath
}

func writeGitHubFixture(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubListAndValidateUsePinnedHostAndActualContext(t *testing.T) {
	provider, repo, fixture, _, argsPath := githubFixture(t)

	prs, err := provider.List(context.Background(), repo)
	if err != nil || len(prs) != 1 {
		t.Fatalf("list: %+v %v", prs, err)
	}
	if prs[0].Number != 42 || prs[0].Head != "main" || prs[0].Base != "release" || prs[0].HeadSHA != fixture.HeadRefOID || prs[0].BaseSHA != fixture.BaseRefOID || prs[0].Repository != "owner/repo" {
		t.Fatalf("PR context fields lost: %+v", prs[0])
	}
	arguments, err := os.ReadFile(argsPath)
	if err != nil || !strings.Contains(string(arguments), "--repo\ngithub.com/owner/repo\n") || !strings.HasPrefix(string(arguments), "pr\nlist\n") {
		t.Fatalf("GitHub list host not pinned: %q %v", arguments, err)
	}

	pr, err := provider.Validate(context.Background(), repo, "main", fixture.HeadRefOID, 42)
	if err != nil || pr == nil || pr.Title != fixture.Title || pr.URL != fixture.URL {
		t.Fatalf("valid PR rejected: %+v %v", pr, err)
	}
	arguments, err = os.ReadFile(argsPath)
	if err != nil || !strings.Contains(string(arguments), "--repo\ngithub.com/owner/repo\n") || !strings.HasPrefix(string(arguments), "pr\nview\n42\n") {
		t.Fatalf("GitHub view host not pinned: %q %v", arguments, err)
	}
}

func TestGitHubRejectsMismatchedClosedForkAndChangedRemote(t *testing.T) {
	provider, repo, baseline, viewPath, _ := githubFixture(t)
	for _, scenario := range []struct {
		name   string
		modify func(*githubPR)
	}{
		{"closed", func(pr *githubPR) { pr.State = "CLOSED" }},
		{"merged", func(pr *githubPR) { pr.State = "MERGED" }},
		{"different number", func(pr *githubPR) { pr.Number = 43 }},
		{"different branch", func(pr *githubPR) { pr.HeadRefName = "feature/other" }},
		{"different commit", func(pr *githubPR) { pr.HeadRefOID = strings.Repeat("b", 40) }},
		{"fork", func(pr *githubPR) { pr.IsCrossRepository = true }},
		{"missing target branch", func(pr *githubPR) { pr.BaseRefName = "" }},
		{"missing target commit", func(pr *githubPR) { pr.BaseRefOID = "" }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pr := baseline
			scenario.modify(&pr)
			writeGitHubFixture(t, viewPath, pr)

			if _, err := provider.Validate(context.Background(), repo, "main", baseline.HeadRefOID, 42); err == nil {
				t.Fatal("inconsistent PR accepted")
			}
		})
	}

	writeGitHubFixture(t, viewPath, baseline)
	testutil.Git(t, repo.Path, "remote", "set-url", "origin", "https://github.com/other/repository.git")
	if _, err := provider.Validate(context.Background(), repo, "main", baseline.HeadRefOID, 42); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("changed source repository accepted: %v", err)
	}
}

func TestGitHubReturnsAccessErrorsEmptyListAndMalformedResponse(t *testing.T) {
	provider, repo, baseline, viewPath, _ := githubFixture(t)
	t.Setenv("FLOWLINE_GH_MODE", "noauth")
	t.Setenv("FLOWLINE_TEST_GITHUB_TOKEN", "opaque-github-fixture-7834")

	_, err := provider.List(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), "authentication failed") || strings.Contains(err.Error(), os.Getenv("FLOWLINE_TEST_GITHUB_TOKEN")) {
		t.Fatalf("access failure missing or credential leaked: %v", err)
	}

	t.Setenv("FLOWLINE_GH_MODE", "")
	writeGitHubFixture(t, os.Getenv("FLOWLINE_GH_LIST"), []githubPR{})
	prs, err := provider.List(context.Background(), repo)
	if err != nil || prs == nil || len(prs) != 0 {
		t.Fatalf("empty list incorrectly treated as error or null: %+v %v", prs, err)
	}
	if err := os.WriteFile(viewPath, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Validate(context.Background(), repo, "main", baseline.HeadRefOID, 42); err == nil {
		t.Fatal("malformed CLI result accepted")
	}

	missing := &Provider{Command: filepath.Join(t.TempDir(), "absent-gh")}
	if _, err := missing.List(context.Background(), repo); err == nil || !strings.Contains(err.Error(), "gh auth login") {
		t.Fatalf("missing CLI is not actionable: %v", err)
	}
}

func TestGitHubRepositoryRemoteParsing(t *testing.T) {
	for _, remote := range []string{"git@github.com:owner/repo.git", "https://github.com/owner/repo.git", "ssh://git@github.com/owner/repo", "https://github.com/owner/repo/"} {
		if got, err := githubRepository(remote); err != nil || got != "owner/repo" {
			t.Errorf("valid remote %q: %q %v", remote, got, err)
		}
	}
	for _, remote := range []string{"", "/local/repo", "https://gitlab.com/owner/repo", "https://github.com.evil.example/owner/repo", "https://github.com/owner/repo?token=x", "https://github.com/owner/repo#fragment", "git@github.com:owner/../repo", "https://github.com/owner/repo/extra"} {
		if _, err := githubRepository(remote); err == nil {
			t.Errorf("unsupported remote accepted: %q", remote)
		}
	}
}
