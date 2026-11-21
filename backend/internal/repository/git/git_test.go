package git

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"flowline/internal/testutil"
)

func TestSnapshotsRejectRedirectedWorktreeMetadata(t *testing.T) {
	for _, operation := range []string{"snapshot", "partial-diff"} {
		for _, target := range []string{"source", "sibling", "branch"} {
			t.Run(operation+"/"+target, func(t *testing.T) {
				repo, base := testutil.Repository(t)
				ctx := context.Background()
				runDir := t.TempDir()
				nodePath, siblingPath := filepath.Join(runDir, "node"), filepath.Join(runDir, "sibling")
				for _, path := range []string{nodePath, siblingPath} {
					if _, err := PrepareWorktree(ctx, repo, path, base, nil); err != nil {
						t.Fatal(err)
					}
				}
				for name, content := range map[string]string{"base.txt": "user dirty\n", "staged.txt": "user staged\n", "untracked.txt": "user untracked\n"} {
					if err := os.WriteFile(filepath.Join(repo.Path, name), []byte(content), 0600); err != nil {
						t.Fatal(err)
					}
				}
				testutil.Git(t, repo.Path, "add", "staged.txt")
				if err := os.WriteFile(filepath.Join(nodePath, "node.txt"), []byte("node edit\n"), 0600); err != nil {
					t.Fatal(err)
				}
				sourceAdmin := testutil.Git(t, repo.Path, "rev-parse", "--absolute-git-dir")
				siblingAdmin := testutil.Git(t, siblingPath, "rev-parse", "--absolute-git-dir")
				before := make(map[string][]byte)
				for _, admin := range []string{sourceAdmin, siblingAdmin} {
					for _, name := range []string{"HEAD", "index"} {
						path := filepath.Join(admin, name)
						data, err := os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						before[path] = data
					}
				}
				sourceSHA := testutil.Git(t, repo.Path, "rev-parse", "HEAD")
				marker := filepath.Join(nodePath, ".git")
				switch target {
				case "source":
					if err := os.WriteFile(marker, []byte("gitdir: "+sourceAdmin+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "sibling":
					if err := os.WriteFile(marker, []byte("gitdir: "+siblingAdmin+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
				case "branch":
					testutil.Git(t, nodePath, "symbolic-ref", "HEAD", "refs/heads/main")
				}

				var err error
				if operation == "snapshot" {
					_, _, err = SnapshotWorktree(ctx, repo, nodePath, base)
				} else {
					_, err = CaptureWorktreeDiff(ctx, repo, nodePath, base)
				}
				if err == nil {
					t.Fatal("accepted redirected worktree metadata")
				}

				for path, expected := range before {
					actual, err := os.ReadFile(path)
					if err != nil || !bytes.Equal(actual, expected) {
						t.Fatalf("metadata changed: %s (%v)", path, err)
					}
				}
				if got := testutil.Git(t, repo.Path, "rev-parse", "HEAD"); got != sourceSHA {
					t.Fatal("source commit moved")
				}
				for name, expected := range map[string]string{"base.txt": "user dirty\n", "staged.txt": "user staged\n", "untracked.txt": "user untracked\n"} {
					actual, err := os.ReadFile(filepath.Join(repo.Path, name))
					if err != nil || string(actual) != expected {
						t.Fatalf("user file changed: %s (%v)", name, err)
					}
				}
			})
		}
	}
}

func TestSnapshotAcceptsCanonicalAliasesAndPinsWorktree(t *testing.T) {
	repo, base := testutil.Repository(t)
	ctx := context.Background()
	parent := t.TempDir()
	nodePath := filepath.Join(parent, "node")
	if _, err := PrepareWorktree(ctx, repo, nodePath, base, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nodePath, "node.txt"), []byte("node edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Path, "base.txt"), []byte("user dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sourcePath := repo.Path
	repo.Path = filepath.Join(parent, "source-alias")
	if err := os.Symlink(sourcePath, repo.Path); err != nil {
		t.Fatal(err)
	}
	nodeAlias := filepath.Join(parent, "node-alias")
	if err := os.Symlink(nodePath, nodeAlias); err != nil {
		t.Fatal(err)
	}
	testutil.Git(t, sourcePath, "config", "extensions.worktreeConfig", "true")
	testutil.Git(t, nodePath, "config", "--worktree", "core.worktree", sourcePath)

	_, diff, err := SnapshotWorktree(ctx, repo, nodeAlias, base)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(diff, "node.txt") || strings.Contains(diff, "user dirty") {
		t.Fatalf("snapshot followed wrong working tree: %s", diff)
	}
	if head := testutil.Git(t, sourcePath, "rev-parse", "HEAD"); head != base {
		t.Fatal("source HEAD moved")
	}
	if staged := testutil.Git(t, sourcePath, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatal("source changes staged")
	}
	data, err := os.ReadFile(filepath.Join(sourcePath, "base.txt"))
	if err != nil || string(data) != "user dirty\n" {
		t.Fatal("source working file changed")
	}
}

func TestWorktreesForkJoinPreserveUserChanges(t *testing.T) {
	repo, base := testutil.Repository(t)
	if err := os.WriteFile(filepath.Join(repo.Path, "base.txt"), []byte("user dirty\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Path, "user.txt"), []byte("untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := testutil.Git(t, repo.Path, "status", "--porcelain=v1")
	runDir := t.TempDir()
	ctx := context.Background()
	commits := make([]string, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, name := range []string{"left", "right"} {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			path := filepath.Join(runDir, name)
			if _, errs[i] = PrepareWorktree(ctx, repo, path, base, nil); errs[i] != nil {
				return
			}
			if errs[i] = os.WriteFile(filepath.Join(path, name+".txt"), []byte(name), 0600); errs[i] != nil {
				return
			}
			commits[i], _, errs[i] = SnapshotWorktree(ctx, repo, path, base)
		}(i, name)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	join := filepath.Join(runDir, "join")
	if _, err := PrepareWorktree(ctx, repo, join, base, commits); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"left", "right"} {
		if _, err := os.Stat(filepath.Join(join, name+".txt")); err != nil {
			t.Fatal(err)
		}
	}
	_, diff, err := SnapshotWorktree(ctx, repo, join, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "left.txt") || !strings.Contains(diff, "right.txt") {
		t.Fatalf("missing join diff: %s", diff)
	}
	if after := testutil.Git(t, repo.Path, "status", "--porcelain=v1"); after != before {
		t.Fatalf("source changed: %q -> %q", before, after)
	}
	data, err := os.ReadFile(filepath.Join(repo.Path, "base.txt"))
	if err != nil || string(data) != "user dirty\n" {
		t.Fatal("user changes lost")
	}
	if head := testutil.Git(t, repo.Path, "rev-parse", "HEAD"); head != base {
		t.Fatal("source HEAD changed")
	}
	if _, _, err := SnapshotWorktree(ctx, repo, repo.Path, base); err == nil {
		t.Fatal("accepted source worktree snapshot")
	}
}

func TestWorktreeJoinConflictIsExplicit(t *testing.T) {
	repo, base := testutil.Repository(t)
	ctx := context.Background()
	runDir := t.TempDir()
	var commits []string
	for _, name := range []string{"left", "right"} {
		path := filepath.Join(runDir, name)
		if _, err := PrepareWorktree(ctx, repo, path, base, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "base.txt"), []byte(name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		sha, _, err := SnapshotWorktree(ctx, repo, path, base)
		if err != nil {
			t.Fatal(err)
		}
		commits = append(commits, sha)
	}
	join := filepath.Join(runDir, "join")
	if _, err := PrepareWorktree(ctx, repo, join, base, commits); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflict: %v", err)
	}
	if _, err := os.Stat(filepath.Join(join, "base.txt")); err != nil {
		t.Fatal("conflict artifacts not preserved")
	}
	if head := testutil.Git(t, repo.Path, "rev-parse", "HEAD"); head != base {
		t.Fatal("source HEAD changed")
	}
}

func TestBranchesAndRemoteSanitization(t *testing.T) {
	repo, sha := testutil.Repository(t)
	testutil.Git(t, repo.Path, "branch", "feature/test")
	testutil.Git(t, repo.Path, "update-ref", "refs/remotes/origin/remote-only", sha)
	testutil.Git(t, repo.Path, "update-ref", "refs/remotes/origin/main", sha)
	testutil.Git(t, repo.Path, "remote", "add", "origin", "https://username:password@example.com/team/repo.git?token=foo")
	repo, err := InspectRepository(repo.Path)
	if err != nil {
		t.Fatal(err)
	}
	if repo.Remote != "https://example.com/team/repo.git" {
		t.Fatalf("credentials retained: %q", repo.Remote)
	}
	branches, err := ListBranches(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 3 {
		t.Fatalf("branches: %#v", branches)
	}
	if got, err := ResolveBranch(context.Background(), repo, "remote-only"); err != nil || got != sha {
		t.Fatalf("resolve: %q %v", got, err)
	}
	if _, err := ResolveBranch(context.Background(), repo, "--help"); err == nil {
		t.Fatal("accepted arbitrary ref")
	}
	if _, err := PrepareWorktree(context.Background(), repo, filepath.Join(t.TempDir(), "bad"), "HEAD", nil); err == nil {
		t.Fatal("accepted non-SHA")
	}
}

func TestSnapshotIgnoresInheritedGitWorktreeRouting(t *testing.T) {
	repo, base := testutil.Repository(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "node")
	if _, err := PrepareWorktree(ctx, repo, path, base, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("node"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Path, "base.txt"), []byte("user dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_WORK_TREE", repo.Path)
	t.Setenv("GIT_DIR", filepath.Join(repo.Path, ".git"))

	if _, _, err := SnapshotWorktree(ctx, repo, path, base); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Git(t, repo.Path, "rev-parse", "HEAD"); got != base {
		t.Fatal("inherited environment changed source HEAD")
	}
	if got := testutil.Git(t, repo.Path, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("source changes staged: %s", got)
	}
}

func TestCaptureWorktreeDiffIncludesUntrackedWithoutCommit(t *testing.T) {
	repo, base := testutil.Repository(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failed-node")
	if _, err := PrepareWorktree(ctx, repo, path, base, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "base.txt"), []byte("partial edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "partial.txt"), []byte("new failure artifact\n"), 0600); err != nil {
		t.Fatal(err)
	}

	diff, err := CaptureWorktreeDiff(ctx, repo, path, base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "partial edit") || !strings.Contains(diff, "partial.txt") {
		t.Fatalf("partial diff missing: %s", diff)
	}
	if head := testutil.Git(t, path, "rev-parse", "HEAD"); head != base {
		t.Fatal("partial capture created a commit")
	}
	if status := testutil.Git(t, repo.Path, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("source changed: %s", status)
	}
	if _, err := CaptureWorktreeDiff(ctx, repo, repo.Path, base); err == nil {
		t.Fatal("partial capture accepted source worktree")
	}
}

func TestBoundedDiffIsExplicitAndKeepsCompleteLines(t *testing.T) {
	diff := strings.Repeat("+a normal patch line\n", 100000)
	bounded := boundedDiff(diff, false)
	if len(bounded) > (1<<20)+100 || !strings.Contains(bounded, "diff truncated") {
		t.Fatalf("diff was not bounded: %d", len(bounded))
	}
	if got := boundedDiff("+line", false); got != "+line\n" {
		t.Fatalf("missing patch newline: %q", got)
	}
}
