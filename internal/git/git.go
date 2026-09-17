// Package git wraps go-git and the git CLI for the gitops-compose deployment
// repository.  It supports:
//   - HTTP basic-auth (original behaviour)
//   - SSH key auth via GIT_SSH_COMMAND (Azure DevOps and similar)
//   - Configurable tracking branch (default: main)
//   - Changed-path detection between two commits
//   - Optional subdirectory scoping (only watch a subtree of the repo)
package git

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitHttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

var (
	ErrPathDoesNotExist = fmt.Errorf("path does not exist")
	ErrHasLocalChanges  = fmt.Errorf("local changes detected")
)

// composeFileNames is Docker Compose's default discovery order.
// https://docs.docker.com/compose/how-tos/file/
var composeFileNames = []string{
	"compose.yaml",
	"compose.yml",
	"docker-compose.yml",
	"docker-compose.yaml",
}

func composeFileRank(base string) (int, bool) {
	for i, name := range composeFileNames {
		if name == base {
			return i, true
		}
	}
	return 0, false
}

// DeploymentRepo represents a local clone of the GitOps deployment repository.
type DeploymentRepo struct {
	// HTTP basic-auth (nil when SSH is used)
	auth *gitHttp.BasicAuth

	// SSH configuration (empty values mean SSH is disabled)
	sshKeyPath        string
	sshKnownHostsPath string

	// Local clone path
	path string

	// Branch being tracked (e.g. "main", "beta")
	branch string

	// Optional subdirectory prefix (relative to repo root, e.g. "deployments").
	// When set, only files under this prefix are considered.  Empty means watch
	// the entire repository.
	deploymentsPrefix string
}

type DeploymentRepoOption func(*DeploymentRepo)

// WithAuth configures HTTP basic-auth credentials.
func WithAuth(username, password string) DeploymentRepoOption {
	return func(r *DeploymentRepo) {
		r.auth = &gitHttp.BasicAuth{
			Username: username,
			Password: password,
		}
	}
}

// WithSSH configures SSH key-based authentication.
// keyPath is the path to the private key file (e.g. /ssh/id_ed25519).
// knownHostsPath is the path to a known_hosts file; if empty the system default
// (~/.ssh/known_hosts) is used.
// Host key verification is always enabled — StrictHostKeyChecking=no is
// intentionally NOT set.
func WithSSH(keyPath, knownHostsPath string) DeploymentRepoOption {
	return func(r *DeploymentRepo) {
		r.sshKeyPath = keyPath
		r.sshKnownHostsPath = knownHostsPath
	}
}

// WithBranch sets the Git branch that the repo should track.
func WithBranch(branch string) DeploymentRepoOption {
	return func(r *DeploymentRepo) {
		if branch != "" {
			r.branch = branch
		}
	}
}

// WithDeploymentsPath restricts change detection and compose-file discovery to
// a subdirectory of the repository (relative path, e.g. "deployments" or
// "infra/compose").  Files outside this prefix are ignored entirely.
// An empty value (the default) watches the whole repository.
func WithDeploymentsPath(subdir string) DeploymentRepoOption {
	return func(r *DeploymentRepo) {
		// Normalise: strip leading/trailing slashes, convert backslashes
		subdir = path.Clean(strings.Trim(filepath.ToSlash(subdir), "/"))
		if subdir == "." {
			subdir = ""
		}
		r.deploymentsPrefix = subdir
	}
}

// NewDeploymentRepo opens the repository at path and applies options.
func NewDeploymentRepo(repoPath string, opts ...DeploymentRepoOption) (*DeploymentRepo, error) {
	if _, err := os.Stat(repoPath); os.IsNotExist(err) {
		return nil, ErrPathDoesNotExist
	}

	if _, err := gogit.PlainOpen(repoPath); err != nil {
		return nil, fmt.Errorf("open repo failed: %w", err)
	}

	repo := &DeploymentRepo{
		path:   repoPath,
		branch: "main",
	}

	for _, opt := range opts {
		opt(repo)
	}

	return repo, nil
}

// sshEnabled returns true when SSH key auth is configured.
func (r *DeploymentRepo) sshEnabled() bool {
	return r.sshKeyPath != ""
}

// gitSSHCommand builds the value for the GIT_SSH_COMMAND environment variable.
// It enforces known-hosts verification and never disables StrictHostKeyChecking.
// The private key path is passed to the ssh binary but is not logged.
func (r *DeploymentRepo) gitSSHCommand() string {
	// Base command — never log the key path in error messages; keep it in env only.
	parts := []string{
		"ssh",
		"-i", r.sshKeyPath,
		"-o", "IdentitiesOnly=yes",
	}
	if r.sshKnownHostsPath != "" {
		parts = append(parts, "-o", "UserKnownHostsFile="+r.sshKnownHostsPath)
	}
	return strings.Join(parts, " ")
}

// cmdWithSSH decorates cmd with GIT_SSH_COMMAND when SSH is configured.
// The private key value is placed in the process environment, not in any log.
func (r *DeploymentRepo) cmdWithSSH(cmd *exec.Cmd) *exec.Cmd {
	if !r.sshEnabled() {
		return cmd
	}
	cmd.Env = append(os.Environ(), "GIT_SSH_COMMAND="+r.gitSSHCommand())
	return cmd
}

// localRef returns the refname for the local tracking branch.
func (r *DeploymentRepo) localRef() plumbing.ReferenceName {
	return plumbing.ReferenceName("refs/heads/" + r.branch)
}

// remoteRef returns the refname for the remote tracking branch.
func (r *DeploymentRepo) remoteRef() plumbing.ReferenceName {
	return plumbing.ReferenceName("refs/remotes/origin/" + r.branch)
}

// VerifyRemoteAccess checks that the remote is reachable.
// For SSH repos it uses the git CLI so GIT_SSH_COMMAND is honoured.
func (r *DeploymentRepo) VerifyRemoteAccess() error {
	if r.sshEnabled() {
		cmd := exec.Command("git", "ls-remote", "--heads", "origin")
		cmd.Dir = r.path
		r.cmdWithSSH(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("remote is not working or SSH auth failed: %w", sanitiseOutput(out))
		}
		return nil
	}

	repo, err := gogit.PlainOpen(r.path)
	if err != nil {
		return fmt.Errorf("open repo failed: %w", err)
	}

	remote, err := repo.Remote("origin")
	if err != nil {
		return fmt.Errorf("get remote failed: %w", err)
	}

	listOptions := &gogit.ListOptions{}
	if r.auth != nil {
		listOptions.Auth = r.auth
	}

	if _, err = remote.List(listOptions); err != nil {
		return fmt.Errorf("remote is not working or auth failed: %w", err)
	}

	return nil
}

// VerifyGitCli confirms that the git CLI is available.
func (r *DeploymentRepo) VerifyGitCli() error {
	cmd := exec.Command("git", "ls-remote", "--heads", "origin")
	cmd.Dir = r.path
	r.cmdWithSSH(cmd)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git cli remote access failed: %w %s", err, sanitiseOutput(out))
	}
	return nil
}

// sanitiseOutput converts command output to an error, stripping trailing
// whitespace.  It does NOT include SSH key material.
func sanitiseOutput(out []byte) error {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return fmt.Errorf("(no output)")
	}
	return fmt.Errorf("%s", msg)
}

// HasChanges fetches the remote and reports whether the remote tracking branch
// is ahead of the local branch.
func (r *DeploymentRepo) HasChanges() (bool, error) {
	repo, err := gogit.PlainOpen(r.path)
	if err != nil {
		return false, fmt.Errorf("open repo failed: %w", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("get worktree failed: %w", err)
	}

	status, err := worktree.Status()
	if err != nil {
		return false, fmt.Errorf("get status failed: %w", err)
	}

	if !status.IsClean() {
		return false, ErrHasLocalChanges
	}

	// Use git CLI fetch when SSH is configured so GIT_SSH_COMMAND is used.
	if r.sshEnabled() {
		cmd := exec.Command("git", "fetch", "origin", r.branch)
		cmd.Dir = r.path
		r.cmdWithSSH(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return false, fmt.Errorf("fetch failed: %w %s", err, sanitiseOutput(out))
		}
	} else {
		fetchOpts := &gogit.FetchOptions{
			RemoteName: "origin",
			Auth:       r.auth,
			Tags:       gogit.NoTags,
			Force:      false,
			Prune:      false,
		}
		if err := repo.Fetch(fetchOpts); err != nil && err != gogit.NoErrAlreadyUpToDate {
			return false, fmt.Errorf("fetch failed: %w", err)
		}
	}

	localRef, err := repo.Reference(r.localRef(), true)
	if err != nil {
		return false, fmt.Errorf("get local ref failed: %w", err)
	}

	remoteRef, err := repo.Reference(r.remoteRef(), true)
	if err != nil {
		return false, fmt.Errorf("get remote ref failed: %w", err)
	}

	return localRef.Hash() != remoteRef.Hash(), nil
}

// ChangedDeploymentDirs returns the set of deployment directories (relative
// to the repository root) that contain at least one file changed between the
// current local HEAD and the remote HEAD.
//
// A deployment directory is the nearest ancestor of a changed file that
// contains a recognised compose file in either commit. A change to
// "beta/payments/config/app.conf" therefore maps to "beta/payments" when that
// directory holds the compose file — not to "beta/payments/config".
//
// If the two commits are identical, or no in-scope compose deployments
// changed, the returned slice is empty.
func (r *DeploymentRepo) ChangedDeploymentDirs() ([]string, error) {
	repo, err := gogit.PlainOpen(r.path)
	if err != nil {
		return nil, fmt.Errorf("open repo failed: %w", err)
	}

	localRef, err := repo.Reference(r.localRef(), true)
	if err != nil {
		return nil, fmt.Errorf("get local ref failed: %w", err)
	}

	remoteRef, err := repo.Reference(r.remoteRef(), true)
	if err != nil {
		return nil, fmt.Errorf("get remote ref failed: %w", err)
	}

	if localRef.Hash() == remoteRef.Hash() {
		return []string{}, nil
	}

	localCommit, err := repo.CommitObject(localRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("get local commit failed: %w", err)
	}

	remoteCommit, err := repo.CommitObject(remoteRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("get remote commit failed: %w", err)
	}

	composeDirs := map[string]struct{}{}
	for _, c := range []*object.Commit{localCommit, remoteCommit} {
		dirs, err := r.composeDirsFromCommit(*c)
		if err != nil {
			return nil, err
		}
		for dir := range dirs {
			composeDirs[dir] = struct{}{}
		}
	}

	patch, err := localCommit.Patch(remoteCommit)
	if err != nil {
		return nil, fmt.Errorf("compute patch failed: %w", err)
	}

	seen := map[string]struct{}{}
	for _, fp := range patch.FilePatches() {
		from, to := fp.Files()
		for _, f := range []object.File{safeFile(from), safeFile(to)} {
			if f == (object.File{}) {
				continue
			}
			// Skip files outside the configured subdirectory prefix.
			if r.deploymentsPrefix != "" {
				if !strings.HasPrefix(f.Name, r.deploymentsPrefix+"/") {
					continue
				}
			}
			dir, ok := nearestComposeDir(f.Name, composeDirs)
			if ok {
				seen[dir] = struct{}{}
			}
		}
	}

	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	return dirs, nil
}

// safeFile converts a nullable diff.File interface to an object.File value.
// Returns the zero value when the interface is nil.
func safeFile(f interface {
	Hash() plumbing.Hash
	Path() string
}) object.File {
	if f == nil {
		return object.File{}
	}
	return object.File{Name: f.Path()}
}

// composeDirsFromCommit returns the set of repository-relative directories that
// contain a recognised compose file in the given commit.
func (r *DeploymentRepo) composeDirsFromCommit(c object.Commit) (map[string]struct{}, error) {
	files, err := r.filterComposeFiles(c)
	if err != nil {
		return nil, err
	}
	dirs := make(map[string]struct{}, len(files))
	for _, fpath := range files {
		rel, err := filepath.Rel(r.path, fpath)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		dir := path.Dir(rel)
		if dir == "." {
			dir = ""
		}
		dirs[dir] = struct{}{}
	}
	return dirs, nil
}

// nearestComposeDir walks from the changed file up to the nearest ancestor
// directory that contains a compose file. ok is false when no compose ancestor
// exists (the change is not part of a deployment).
func nearestComposeDir(filePath string, composeDirs map[string]struct{}) (string, bool) {
	dir := path.Dir(filepath.ToSlash(filePath))
	if dir == "." {
		dir = ""
	}
	for {
		if _, ok := composeDirs[dir]; ok {
			return dir, true
		}
		if dir == "" {
			return "", false
		}
		parent := path.Dir(dir)
		if parent == dir || parent == "." {
			if _, ok := composeDirs[""]; ok {
				return "", true
			}
			return "", false
		}
		dir = parent
	}
}

// filterComposeFiles returns the full filesystem paths of all compose files in
// the commit tree rooted at the given commit. Recognised names are compose.yaml,
// compose.yml, docker-compose.yml, and docker-compose.yaml. When several exist
// in the same directory, the Docker Compose preference order is used.
func (r *DeploymentRepo) filterComposeFiles(c object.Commit) ([]string, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, fmt.Errorf("get tree failed: %w", err)
	}

	// dir → preferred file (compose.yaml wins over docker-compose.yml)
	dirToFile := map[string]string{}

	err = tree.Files().ForEach(func(f *object.File) error {
		// Skip files outside the configured subdirectory prefix.
		if r.deploymentsPrefix != "" {
			if !strings.HasPrefix(f.Name, r.deploymentsPrefix+"/") {
				return nil
			}
		}

		base := path.Base(f.Name)
		dir := path.Dir(f.Name)
		if dir == "." {
			dir = ""
		}

		rank, ok := composeFileRank(base)
		if !ok {
			return nil
		}
		full := filepath.Join(r.path, f.Name)
		if existing, exists := dirToFile[dir]; exists {
			existingRank, _ := composeFileRank(path.Base(existing))
			if rank >= existingRank {
				return nil
			}
		}
		dirToFile[dir] = full
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk tree failed: %w", err)
	}

	files := make([]string, 0, len(dirToFile))
	for _, fpath := range dirToFile {
		files = append(files, fpath)
	}
	return files, nil
}

// GetRemoteComposeFiles returns compose file paths from the remote HEAD.
func (r *DeploymentRepo) GetRemoteComposeFiles() ([]string, error) {
	repo, err := gogit.PlainOpen(r.path)
	if err != nil {
		return nil, fmt.Errorf("open repo failed: %w", err)
	}

	ref, err := repo.Reference(r.remoteRef(), true)
	if err != nil {
		return nil, fmt.Errorf("get remote ref failed: %w", err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("get commit object failed: %w", err)
	}

	return r.filterComposeFiles(*commit)
}

// GetLocalComposeFiles returns compose file paths from the local HEAD.
func (r *DeploymentRepo) GetLocalComposeFiles() ([]string, error) {
	repo, err := gogit.PlainOpen(r.path)
	if err != nil {
		return nil, fmt.Errorf("open repo failed: %w", err)
	}

	ref, err := repo.Reference(r.localRef(), true)
	if err != nil {
		return nil, fmt.Errorf("get local ref failed: %w", err)
	}

	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		return nil, fmt.Errorf("get commit object failed: %w", err)
	}

	return r.filterComposeFiles(*commit)
}

// Pull fast-forwards the local branch to the remote HEAD.
// TODO: Replace exec with go-git once https://github.com/go-git/go-git/pull/1235 is resolved.
func (r *DeploymentRepo) Pull() error {
	cmd := exec.Command("git", "pull", "origin", r.branch)
	cmd.Dir = r.path
	r.cmdWithSSH(cmd)

	output, err := cmd.CombinedOutput()
	if err != nil {
		outStr := strings.TrimSpace(string(output))
		if outStr == "Already up to date." || outStr == "Already up-to-date." {
			return nil
		}
		return fmt.Errorf("pull failed: %w %s", err, outStr)
	}

	return nil
}
