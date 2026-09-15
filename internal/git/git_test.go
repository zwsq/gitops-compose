package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// initBareRepo creates a bare repository at bareDir and clones it to cloneDir.
// Returns a *DeploymentRepo tracking the given branch.
func initBareAndClone(t *testing.T, branch string) (bareDir, cloneDir string, repo *DeploymentRepo) {
	t.Helper()

	bareDir = t.TempDir()
	cloneDir = t.TempDir()

	// Init bare repo
	mustRun(t, bareDir, "git", "init", "--bare", "--initial-branch="+branch)

	// Clone it
	mustRun(t, t.TempDir(), "git", "clone", bareDir, cloneDir)

	// Configure identity in clone
	mustRun(t, cloneDir, "git", "config", "user.email", "test@example.com")
	mustRun(t, cloneDir, "git", "config", "user.name", "Test")

	// Make an initial commit so the branch exists
	writeFile(t, cloneDir, "README.md", "initial")
	mustRun(t, cloneDir, "git", "add", ".")
	mustRun(t, cloneDir, "git", "commit", "-m", "initial")
	mustRun(t, cloneDir, "git", "push", "-u", "origin", branch)

	r, err := NewDeploymentRepo(cloneDir, WithBranch(branch))
	if err != nil {
		t.Fatalf("NewDeploymentRepo: %v", err)
	}
	return bareDir, cloneDir, r
}

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %v failed: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pushCommit writes file rel with content to cloneDir and pushes.
func pushCommit(t *testing.T, cloneDir, rel, content, branch string) {
	t.Helper()
	writeFile(t, cloneDir, rel, content)
	mustRun(t, cloneDir, "git", "add", rel)
	mustRun(t, cloneDir, "git", "commit", "-m", "update "+rel)
	mustRun(t, cloneDir, "git", "push", "origin", branch)
}

// makeSecondClone creates a second clone that represents the "remote pusher"
// and returns a *DeploymentRepo for the first clone (which will fetch updates).
func makeSecondClone(t *testing.T, bareDir, branch string) (secondCloneDir string) {
	t.Helper()
	secondCloneDir = t.TempDir()
	mustRun(t, t.TempDir(), "git", "clone", bareDir, secondCloneDir)
	mustRun(t, secondCloneDir, "git", "config", "user.email", "ci@example.com")
	mustRun(t, secondCloneDir, "git", "config", "user.name", "CI")
	mustRun(t, secondCloneDir, "git", "checkout", branch)
	return secondCloneDir
}

// ---------------------------------------------------------------------------
// WithBranch option
// ---------------------------------------------------------------------------

func TestWithBranch(t *testing.T) {
	_, cloneDir, repo := initBareAndClone(t, "main")

	if repo.branch != "main" {
		t.Errorf("expected branch 'main', got %q", repo.branch)
	}

	// Re-open with explicit branch
	repo2, err := NewDeploymentRepo(cloneDir, WithBranch("beta"))
	if err != nil {
		t.Fatal(err)
	}
	if repo2.branch != "beta" {
		t.Errorf("expected branch 'beta', got %q", repo2.branch)
	}
}

func TestWithBranch_Default(t *testing.T) {
	_, cloneDir, _ := initBareAndClone(t, "main")

	// No WithBranch option → default "main"
	repo, err := NewDeploymentRepo(cloneDir)
	if err != nil {
		t.Fatal(err)
	}
	if repo.branch != "main" {
		t.Errorf("expected default branch 'main', got %q", repo.branch)
	}
}

// ---------------------------------------------------------------------------
// HasChanges
// ---------------------------------------------------------------------------

func TestHasChanges_NoChanges(t *testing.T) {
	_, _, repo := initBareAndClone(t, "main")

	has, err := repo.HasChanges()
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("expected no changes on fresh clone")
	}
}

func TestHasChanges_WithNewCommit(t *testing.T) {
	bareDir, cloneDir, repo := initBareAndClone(t, "main")

	// A second clone pushes a change
	second := makeSecondClone(t, bareDir, "main")
	pushCommit(t, second, "beta/payments/.env", "PAYMENTS_IMAGE=registry.example.com/payments:1.42.8", "main")

	// Our repo should detect the remote is ahead
	has, err := repo.HasChanges()
	if err != nil {
		t.Fatalf("HasChanges: %v (cloneDir=%s)", err, cloneDir)
	}
	if !has {
		t.Error("expected changes to be detected")
	}
}

// ---------------------------------------------------------------------------
// ChangedDeploymentDirs
// ---------------------------------------------------------------------------

func TestChangedDeploymentDirs_EnvChange(t *testing.T) {
	bareDir, cloneDir, repo := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	pushCommit(t, second, "beta/payments/.env", "PAYMENTS_IMAGE=registry.example.com/payments:1.42.8", "main")

	// Fetch so remote ref advances
	mustRun(t, cloneDir, "git", "fetch", "origin", "main")

	dirs, err := repo.ChangedDeploymentDirs()
	if err != nil {
		t.Fatal(err)
	}
	if !containsDir(dirs, "beta/payments") {
		t.Errorf("expected 'beta/payments' in changed dirs, got %v", dirs)
	}
}

func TestChangedDeploymentDirs_ComposeFileChange(t *testing.T) {
	bareDir, cloneDir, repo := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	pushCommit(t, second, "beta/payments/compose.yaml", "services: {}", "main")
	mustRun(t, cloneDir, "git", "fetch", "origin", "main")

	dirs, err := repo.ChangedDeploymentDirs()
	if err != nil {
		t.Fatal(err)
	}
	if !containsDir(dirs, "beta/payments") {
		t.Errorf("expected 'beta/payments' in dirs, got %v", dirs)
	}
}

func TestChangedDeploymentDirs_NestedFile(t *testing.T) {
	bareDir, cloneDir, repo := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	// A nested config file change
	pushCommit(t, second, "beta/payments/config/app.conf", "[app]\nport=8080", "main")
	mustRun(t, cloneDir, "git", "fetch", "origin", "main")

	dirs, err := repo.ChangedDeploymentDirs()
	if err != nil {
		t.Fatal(err)
	}
	// "beta/payments/config" — the immediate parent of the file
	// Our deploymentDirFromPath returns path.Dir which is "beta/payments/config"
	found := false
	for _, d := range dirs {
		if strings.HasPrefix(d, "beta/payments") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a dir under beta/payments, got %v", dirs)
	}
}

func TestChangedDeploymentDirs_FrontendDoesNotAffectPayments(t *testing.T) {
	bareDir, cloneDir, repo := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	// Only beta/frontend changes
	pushCommit(t, second, "beta/frontend/.env", "FRONTEND_IMAGE=registry.example.com/frontend:2.0.0", "main")
	mustRun(t, cloneDir, "git", "fetch", "origin", "main")

	dirs, err := repo.ChangedDeploymentDirs()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if strings.Contains(d, "payments") {
			t.Errorf("payments should NOT appear in changed dirs when only frontend changed, got %v", dirs)
		}
	}
	if !containsDir(dirs, "beta/frontend") {
		t.Errorf("expected 'beta/frontend' in dirs, got %v", dirs)
	}
}

func TestChangedDeploymentDirs_NoChanges(t *testing.T) {
	_, _, repo := initBareAndClone(t, "main")

	dirs, err := repo.ChangedDeploymentDirs()
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Errorf("expected empty dirs when no changes, got %v", dirs)
	}
}

func TestChangedDeploymentDirs_PrefixFiltersOutOtherDirs(t *testing.T) {
	bareDir, cloneDir, _ := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	// Change is outside the watched prefix
	pushCommit(t, second, "other/service/.env", "IMG=v2", "main")
	mustRun(t, cloneDir, "git", "fetch", "origin", "main")

	repo, err := NewDeploymentRepo(cloneDir, WithBranch("main"), WithDeploymentsPath("deployments"))
	if err != nil {
		t.Fatal(err)
	}

	dirs, err := repo.ChangedDeploymentDirs()
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 0 {
		t.Errorf("expected no dirs when change is outside prefix, got %v", dirs)
	}
}

func TestChangedDeploymentDirs_PrefixAllowsMatchingDir(t *testing.T) {
	bareDir, cloneDir, _ := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	pushCommit(t, second, "deployments/payments/.env", "IMG=v2", "main")
	mustRun(t, cloneDir, "git", "fetch", "origin", "main")

	repo, err := NewDeploymentRepo(cloneDir, WithBranch("main"), WithDeploymentsPath("deployments"))
	if err != nil {
		t.Fatal(err)
	}

	dirs, err := repo.ChangedDeploymentDirs()
	if err != nil {
		t.Fatal(err)
	}
	if !containsDir(dirs, "deployments/payments") {
		t.Errorf("expected 'deployments/payments' in dirs, got %v", dirs)
	}
}

// ---------------------------------------------------------------------------
// deploymentDirFromPath unit tests (pure function, no I/O)
// ---------------------------------------------------------------------------

func TestDeploymentDirFromPath(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"beta/payments/.env", "beta/payments"},
		{"beta/payments/compose.yaml", "beta/payments"},
		{"beta/payments/config/app.conf", "beta/payments/config"},
		{"beta/frontend/.env", "beta/frontend"},
		{"README.md", ""},
		{"root-file.txt", ""},
	}
	for _, tc := range cases {
		got := deploymentDirFromPath(tc.input)
		if got != tc.expected {
			t.Errorf("deploymentDirFromPath(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// ---------------------------------------------------------------------------
// SSH configuration (unit tests — no real SSH connection)
// ---------------------------------------------------------------------------

func TestWithSSH_SetsFields(t *testing.T) {
	_, cloneDir, _ := initBareAndClone(t, "main")

	repo, err := NewDeploymentRepo(cloneDir, WithSSH("/ssh/id_ed25519", "/ssh/known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	if !repo.sshEnabled() {
		t.Error("sshEnabled() should return true when key is set")
	}
	if repo.sshKeyPath != "/ssh/id_ed25519" {
		t.Errorf("unexpected sshKeyPath: %q", repo.sshKeyPath)
	}
	if repo.sshKnownHostsPath != "/ssh/known_hosts" {
		t.Errorf("unexpected sshKnownHostsPath: %q", repo.sshKnownHostsPath)
	}
}

func TestSSHCommand_DoesNotContainStrictHostKeyCheckingNo(t *testing.T) {
	_, cloneDir, _ := initBareAndClone(t, "main")

	repo, err := NewDeploymentRepo(cloneDir, WithSSH("/ssh/id_ed25519", "/ssh/known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := repo.gitSSHCommand()

	if strings.Contains(cmd, "StrictHostKeyChecking=no") {
		t.Error("SSH command must NOT contain StrictHostKeyChecking=no")
	}
	if !strings.Contains(cmd, "-i") {
		t.Error("SSH command should contain -i flag")
	}
	if !strings.Contains(cmd, "IdentitiesOnly=yes") {
		t.Error("SSH command should contain IdentitiesOnly=yes")
	}
	if !strings.Contains(cmd, "/ssh/known_hosts") {
		t.Error("SSH command should reference known_hosts file")
	}
}

func TestSSHCommand_WithoutKnownHosts(t *testing.T) {
	_, cloneDir, _ := initBareAndClone(t, "main")

	repo, err := NewDeploymentRepo(cloneDir, WithSSH("/ssh/id_ed25519", ""))
	if err != nil {
		t.Fatal(err)
	}
	cmd := repo.gitSSHCommand()

	// Without an explicit known_hosts, don't add UserKnownHostsFile
	if strings.Contains(cmd, "UserKnownHostsFile") {
		t.Error("should not set UserKnownHostsFile when knownHostsPath is empty")
	}
}

func TestSSHNotEnabled_WhenNoKey(t *testing.T) {
	_, cloneDir, _ := initBareAndClone(t, "main")

	repo, err := NewDeploymentRepo(cloneDir)
	if err != nil {
		t.Fatal(err)
	}
	if repo.sshEnabled() {
		t.Error("sshEnabled() should return false when no key is set")
	}
}

// ---------------------------------------------------------------------------
// filterComposeFiles — compose.yaml preference
// ---------------------------------------------------------------------------

func TestFilterComposeFiles_PreferComposeYaml(t *testing.T) {
	bareDir, cloneDir, repo := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	// Push both files in the same directory
	pushCommit(t, second, "beta/payments/compose.yaml", "services: {}", "main")
	pushCommit(t, second, "beta/payments/docker-compose.yml", "services: {}", "main")
	mustRun(t, cloneDir, "git", "pull", "origin", "main")

	goRepo, _ := gogit.PlainOpen(cloneDir)
	ref, _ := goRepo.Reference(repo.localRef(), true)
	commit, _ := goRepo.CommitObject(ref.Hash())

	files, err := repo.filterComposeFiles(*commit)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		base := filepath.Base(f)
		dir := filepath.Base(filepath.Dir(f))
		if dir == "payments" && base == "docker-compose.yml" {
			t.Error("docker-compose.yml should not be returned when compose.yaml exists in same dir")
		}
	}

	found := false
	for _, f := range files {
		if filepath.Base(f) == "compose.yaml" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected compose.yaml in result, got: %v", files)
	}
}

func TestFilterComposeFiles_FallbackToDockerComposeYml(t *testing.T) {
	bareDir, cloneDir, repo := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	pushCommit(t, second, "beta/reporting/docker-compose.yml", "services: {}", "main")
	mustRun(t, cloneDir, "git", "pull", "origin", "main")

	goRepo, _ := gogit.PlainOpen(cloneDir)
	ref, _ := goRepo.Reference(repo.localRef(), true)
	commit, _ := goRepo.CommitObject(ref.Hash())

	files, err := repo.filterComposeFiles(*commit)
	if err != nil {
		t.Fatal(err)
	}

	found := false
	for _, f := range files {
		if filepath.Base(f) == "docker-compose.yml" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected docker-compose.yml in result, got: %v", files)
	}
}

// ---------------------------------------------------------------------------
// NewDeploymentRepo — error cases
// ---------------------------------------------------------------------------

func TestNewDeploymentRepo_PathDoesNotExist(t *testing.T) {
	_, err := NewDeploymentRepo("/nonexistent/path/abc123")
	if err != ErrPathDoesNotExist {
		t.Errorf("expected ErrPathDoesNotExist, got %v", err)
	}
}

func TestNewDeploymentRepo_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	_, err := NewDeploymentRepo(dir)
	if err == nil {
		t.Error("expected error for directory that is not a git repo")
	}
}

// ---------------------------------------------------------------------------
// Pull — branch-aware
// ---------------------------------------------------------------------------

func TestPull_BranchAware(t *testing.T) {
	bareDir, cloneDir, repo := initBareAndClone(t, "main")
	second := makeSecondClone(t, bareDir, "main")

	pushCommit(t, second, "beta/payments/.env", "IMG=v2", "main")

	if err := repo.Pull(); err != nil {
		t.Fatalf("Pull: %v (cloneDir=%s)", err, cloneDir)
	}

	// The file should now exist locally
	if _, err := os.Stat(filepath.Join(cloneDir, "beta/payments/.env")); err != nil {
		t.Error("expected beta/payments/.env to exist after pull")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func containsDir(dirs []string, target string) bool {
	for _, d := range dirs {
		if d == target {
			return true
		}
	}
	return false
}
