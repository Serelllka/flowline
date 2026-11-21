package git

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"flowline/internal/cli/process"
	"flowline/internal/model"
)

var commitPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?$`)

func gitCommand(ctx context.Context, dir string, args ...string) (string, error) {
	argv := []string{"git", "-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.fsmonitor=false", "-c", "user.name=Flowline", "-c", "user.email=flowline@localhost", "-c", "credential.interactive=false", "-c", "core.pager=cat"}
	argv = append(argv, args...)
	// Never let inherited Git routing variables redirect mutations to the user tree.
	envArgs := []string{"env"}
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(name, "GIT_") {
			envArgs = append(envArgs, "-u", name)
		}
	}
	envArgs = append(envArgs, "GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=Flowline", "GIT_AUTHOR_EMAIL=flowline@localhost", "GIT_COMMITTER_NAME=Flowline", "GIT_COMMITTER_EMAIL=flowline@localhost")
	argv = append(envArgs, argv...)
	var detail strings.Builder
	out, _, err := process.RunProcess(ctx, argv, dir, "", func(line string) {
		if detail.Len() < 32<<10 {
			detail.WriteString(line)
		}
	})
	if err != nil {
		return out, fmt.Errorf("git %s: %w: %s", args[0], err, process.Redact(strings.TrimSpace(detail.String())))
	}

	return strings.TrimSpace(out), nil
}

func InspectRepository(path string) (model.Repository, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	absolute, err := filepath.Abs(path)
	if err != nil {
		return model.Repository{}, err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return model.Repository{}, fmt.Errorf("repository path: %w", err)
	}
	bare, err := gitCommand(ctx, absolute, "rev-parse", "--is-bare-repository")
	if err != nil {
		return model.Repository{}, err
	}
	if bare != "false" {
		return model.Repository{}, errors.New("bare repositories are not supported")
	}
	root, err := gitCommand(ctx, absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		return model.Repository{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return model.Repository{}, err
	}
	remote, remoteErr := gitCommand(ctx, root, "config", "--get", "remote.origin.url")
	if remoteErr != nil {
		remote = ""
	}
	if parsed, parseErr := url.Parse(remote); parseErr == nil && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		remote = parsed.String()
	}

	return model.Repository{Name: filepath.Base(root), Path: root, Remote: process.Redact(remote)}, nil
}

func ListBranches(ctx context.Context, repo model.Repository) ([]model.Branch, error) {
	out, err := gitCommand(ctx, repo.Path, "for-each-ref", "--format=%(refname)%09%(objectname)", "refs/heads/", "refs/remotes/origin/")
	if err != nil {
		return nil, err
	}
	byName := make(map[string]model.Branch)
	for _, line := range strings.Split(out, "\n") {
		ref, sha, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasPrefix(ref, "refs/heads/") {
			continue
		}
		name := strings.TrimPrefix(ref, "refs/heads/")
		byName[name] = model.Branch{Name: name, SHA: sha}
	}
	for _, line := range strings.Split(out, "\n") {
		ref, sha, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasPrefix(ref, "refs/remotes/origin/") {
			continue
		}
		name := strings.TrimPrefix(ref, "refs/remotes/origin/")
		if name == "HEAD" {
			continue
		}
		if _, exists := byName[name]; !exists {
			byName[name] = model.Branch{Name: name, SHA: sha}
		}
	}
	branches := make([]model.Branch, 0, len(byName))
	for _, branch := range byName {
		branches = append(branches, branch)
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].Name < branches[j].Name })

	return branches, nil
}

func ResolveBranch(ctx context.Context, repo model.Repository, name string) (string, error) {
	branches, err := ListBranches(ctx, repo)
	if err != nil {
		return "", err
	}
	for _, branch := range branches {
		if branch.Name == name {
			return branch.SHA, nil
		}
	}

	return "", fmt.Errorf("branch %q does not exist in this repository", name)
}

func verifiedCommit(ctx context.Context, repo model.Repository, sha string) (string, error) {
	if !commitPattern.MatchString(sha) {
		return "", errors.New("expected a full commit SHA")
	}
	return gitCommand(ctx, repo.Path, "rev-parse", "--verify", sha+"^{commit}")
}

func PrepareWorktree(ctx context.Context, repo model.Repository, path, baseSHA string, parentCommits []string) (string, error) {
	base, err := verifiedCommit(ctx, repo, baseSHA)
	if err != nil {
		return "", err
	}
	parents := make([]string, 0, len(parentCommits))
	seen := make(map[string]bool)
	for _, sha := range parentCommits {
		commit, err := verifiedCommit(ctx, repo, sha)
		if err != nil {
			return "", err
		}
		if !seen[commit] {
			parents = append(parents, commit)
			seen[commit] = true
		}
	}
	if len(parents) > 0 {
		base = parents[0]
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err = os.Lstat(absolute); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("run worktree path already exists or is inaccessible")
	}
	if err = os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
		return "", err
	}
	if _, err = gitCommand(ctx, repo.Path, "worktree", "add", "--detach", "--", absolute, base); err != nil {
		return "", err
	}
	for i, sha := range parents {
		if i == 0 {
			continue
		}
		if _, err = gitCommand(ctx, absolute, "merge", "--no-edit", "--no-gpg-sign", "--no-ff", "-m", "Flowline: combine predecessor results", sha); err != nil {
			return "", fmt.Errorf("cannot combine predecessor changes; resolve conflict in %s: %w", absolute, err)
		}
	}

	return gitCommand(ctx, absolute, "rev-parse", "HEAD")
}

type isolatedGitWorktree struct {
	path   string
	gitDir string
}

func (w isolatedGitWorktree) command(ctx context.Context, args ...string) (string, error) {
	// Pin both paths after verification instead of rediscovering a mutable .git
	// marker or honoring a repository's core.worktree setting during mutations.
	pinned := []string{"--git-dir=" + w.gitDir, "--work-tree=" + w.path}

	return gitCommand(ctx, w.path, append(pinned, args...)...)
}

func metadataPath(base, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty worktree metadata path")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(base, value)
	}

	return filepath.EvalSymlinks(value)
}

func regularMetadata(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("worktree metadata must be a regular file")
	}

	return os.ReadFile(path)
}

func isolatedWorktree(ctx context.Context, repo model.Repository, path, baseSHA string) (isolatedGitWorktree, error) {
	invalid := isolatedGitWorktree{}
	if _, err := verifiedCommit(ctx, repo, baseSHA); err != nil {
		return invalid, err
	}

	absolute, err := filepath.Abs(path)
	if err != nil {
		return invalid, err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return invalid, err
	}

	source, err := filepath.EvalSymlinks(repo.Path)
	if err != nil {
		return invalid, err
	}
	if canonical == source {
		return invalid, errors.New("refusing to snapshot the user's source worktree")
	}

	markerPath := filepath.Join(canonical, ".git")
	marker, err := regularMetadata(markerPath)
	if err != nil || !strings.HasPrefix(string(marker), "gitdir: ") {
		return invalid, errors.New("snapshot requires an isolated linked worktree")
	}
	gitDir, err := metadataPath(canonical, strings.TrimPrefix(string(marker), "gitdir: "))
	if err != nil {
		return invalid, err
	}

	expected, err := gitCommand(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return invalid, err
	}
	expected, err = metadataPath(source, expected)
	if err != nil {
		return invalid, err
	}
	administration, err := filepath.EvalSymlinks(filepath.Join(expected, "worktrees"))
	if err != nil || filepath.Dir(gitDir) != administration {
		return invalid, errors.New("Git directory is not a linked worktree administration directory")
	}

	commonData, err := regularMetadata(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return invalid, err
	}
	common, err := metadataPath(gitDir, string(commonData))
	if err != nil || common != expected {
		return invalid, errors.New("worktree belongs to another repository")
	}

	backlinkData, err := regularMetadata(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return invalid, err
	}
	backlink, err := metadataPath(gitDir, string(backlinkData))
	if err != nil || backlink != markerPath {
		return invalid, errors.New("Git administration directory belongs to another worktree")
	}

	head, err := regularMetadata(filepath.Join(gitDir, "HEAD"))
	if err != nil || !commitPattern.MatchString(strings.TrimSpace(string(head))) {
		return invalid, errors.New("snapshot requires detached HEAD; shared branches must not be changed")
	}

	return isolatedGitWorktree{path: canonical, gitDir: gitDir}, nil
}

func boundedDiff(diff string, truncated bool) string {
	diff = process.Redact(diff)
	if len(diff) > 1<<20 {
		diff = diff[:1<<20]
		truncated = true
	}
	if truncated {
		// Do not display an incomplete final line (which could contain a partial secret).
		if last := strings.LastIndexByte(diff, '\n'); last >= 0 {
			diff = diff[:last+1]
		} else {
			diff = ""
		}
		return diff + "\n[diff truncated at 1 MiB; full changes remain in the worktree]\n"
	}
	if diff != "" && !strings.HasSuffix(diff, "\n") {
		diff += "\n"
	}

	return diff
}

// CaptureWorktreeDiff stages only the isolated worktree's changes to include new
// files, but does not commit or move HEAD. It also preserves conflict marker text.
func CaptureWorktreeDiff(ctx context.Context, repo model.Repository, path, baseSHA string) (string, error) {
	worktree, err := isolatedWorktree(ctx, repo, path, baseSHA)
	if err != nil {
		return "", err
	}

	if _, err = worktree.command(ctx, "add", "-A", "--", "."); err != nil {
		return "", err
	}

	diff, err := worktree.command(ctx, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--binary", baseSHA, "--")
	if err != nil && !errors.Is(err, process.ErrOutputLimit) {
		return "", err
	}

	return boundedDiff(diff, errors.Is(err, process.ErrOutputLimit)), nil
}

// SnapshotWorktree only stages and commits in a verified linked worktree.
func SnapshotWorktree(ctx context.Context, repo model.Repository, path, baseSHA string) (string, string, error) {
	worktree, err := isolatedWorktree(ctx, repo, path, baseSHA)
	if err != nil {
		return "", "", err
	}

	if _, err = worktree.command(ctx, "add", "-A", "--", "."); err != nil {
		return "", "", err
	}

	changed, err := worktree.command(ctx, "diff", "--cached", "--name-only", "--no-ext-diff")
	if err != nil {
		return "", "", err
	}

	if changed != "" {
		if _, err = worktree.command(ctx, "commit", "--no-gpg-sign", "-m", "Flowline: block result"); err != nil {
			return "", "", err
		}
	}

	commit, err := worktree.command(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}

	diff, err := worktree.command(ctx, "diff", "--no-ext-diff", "--no-textconv", "--binary", baseSHA, commit, "--")
	if err != nil && !errors.Is(err, process.ErrOutputLimit) {
		return "", "", err
	}

	return commit, boundedDiff(diff, errors.Is(err, process.ErrOutputLimit)), nil
}
