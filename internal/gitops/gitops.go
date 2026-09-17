// Package gitops is the main reconciliation loop: it compares local and remote
// Git state, identifies which deployments changed, and runs docker-compose up.
package gitops

import (
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"github.com/korbiniankuhn/gitops-compose/internal/deployment"
	"github.com/korbiniankuhn/gitops-compose/internal/docker"
	"github.com/korbiniankuhn/gitops-compose/internal/git"
	"github.com/korbiniankuhn/gitops-compose/internal/metrics"
)

type GitOps struct {
	repo             *git.DeploymentRepo
	docker           *docker.Docker
	metrics          *metrics.Metrics
	retryDeployments []*deployment.Deployment
	isFirstCheck     bool
}

func NewGitOps(repo *git.DeploymentRepo, docker *docker.Docker, metrics *metrics.Metrics) *GitOps {
	return &GitOps{
		repo:             repo,
		docker:           docker,
		metrics:          metrics,
		retryDeployments: []*deployment.Deployment{},
		isFirstCheck:     true,
	}
}

func (g *GitOps) applyDeploymentChange(d *deployment.Deployment, state *metrics.DeploymentState) {
	wasChanged, err := d.Apply()

	var operation string
	switch d.State {
	case deployment.Added:
		operation = "start"
	case deployment.Updated:
		operation = "update"
	case deployment.Removed:
		operation = "remove"
	case deployment.Unchanged:
		operation = "unchanged"
	default:
		operation = "unknown"
	}

	if err == deployment.ErrInvalidComposeFile {
		state.Invalid++
		slog.Error("invalid compose file", "file", d.Filepath)
		return
	} else if err != nil {
		state.Failed++
		if d.State == deployment.Unchanged {
			slog.Error("error checking unchanged deployment", "file", d.Filepath, "err", err)
		} else {
			slog.Error("error applying deployment change", "file", d.Filepath, "operation", operation, "err", err)
		}
		return
	}

	switch d.State {
	case deployment.Added:
		if wasChanged {
			state.Started++
			slog.Info("started new deployment", "file", d.Filepath)
		} else {
			state.Unchanged++
			slog.Warn("new deployment was already running", "file", d.Filepath)
		}
	case deployment.Updated:
		if wasChanged {
			state.Updated++
			slog.Info("updated deployment", "file", d.Filepath)
		} else {
			state.Unchanged++
			slog.Warn("updated deployment was already running", "file", d.Filepath)
		}
	case deployment.Removed:
		if wasChanged {
			state.Stopped++
			slog.Info("stopped removed deployment", "file", d.Filepath)
		} else {
			state.Unchanged++
			slog.Warn("removed deployment was not running", "file", d.Filepath)
		}
	case deployment.Unchanged:
		if wasChanged {
			state.Started++
			slog.Warn("started unchanged but not running deployment", "file", d.Filepath)
		} else {
			state.Unchanged++
		}
	}
}

// matchesChangedDirs returns true when the deployment's directory path ends
// with (or equals) one of the changed directory segments.
//
// changedDirs contains relative paths from the repository root such as
// "beta/payments".  d.Dir() is an absolute filesystem path such as
// "/deployments/beta/payments".  We check suffix-match so the mapping works
// regardless of the clone mount point.
func matchesChangedDirs(d *deployment.Deployment, changedDirs []string) bool {
	if len(changedDirs) == 0 {
		return false
	}
	deployDir := filepath.ToSlash(d.Dir())
	for _, changed := range changedDirs {
		changed = filepath.ToSlash(changed)
		if deployDir == changed || strings.HasSuffix(deployDir, "/"+changed) {
			return true
		}
	}
	slog.Debug("deployment not in changed dirs", "deployment", deployDir, "changedDirs", changedDirs)
	return false
}

func (g *GitOps) isRetry(d *deployment.Deployment) bool {
	for _, r := range g.retryDeployments {
		if r.Filepath == d.Filepath {
			return true
		}
	}
	return false
}

func shouldRetry(d *deployment.Deployment) bool {
	if d == nil || d.Error == nil {
		return false
	}
	return d.Error != deployment.ErrInvalidComposeFile
}

func (g *GitOps) inScope(d *deployment.Deployment, changedDirs []string, incremental bool) bool {
	if !incremental {
		return true
	}
	return matchesChangedDirs(d, changedDirs) || g.isRetry(d)
}

// checkAndUpdateDeployments reconciles local compose deployments against the
// remote Git state.
//
// When incremental is false (first-run / error fallback), every deployment is
// reconciled. When incremental is true, only deployments in changedDirs plus
// previously failed retries are reconciled.
func (g *GitOps) checkAndUpdateDeployments(
	state *metrics.DeploymentState,
	changedDirs []string,
	incremental bool,
) ([]*deployment.Deployment, error) {

	localComposeFiles, err := g.repo.GetLocalComposeFiles()
	if err != nil {
		slog.Error("error getting local compose files", "err", err)
		return []*deployment.Deployment{}, err
	}

	remoteComposeFiles, err := g.repo.GetRemoteComposeFiles()
	if err != nil {
		slog.Error("error getting remote compose files", "err", err)
		return []*deployment.Deployment{}, err
	}

	// Build the full deployment list
	deployments := []*deployment.Deployment{}
	for _, localFile := range localComposeFiles {
		d := deployment.NewDeployment(g.docker, localFile)

		if err := d.LoadConfig(); err != nil {
			slog.Error("error loading deployment config", "file", d.Filepath, "err", err)
		}

		if !slices.Contains(remoteComposeFiles, localFile) {
			d.State = deployment.Removed
		}
		deployments = append(deployments, d)
	}
	for _, remoteFile := range remoteComposeFiles {
		if !slices.Contains(localComposeFiles, remoteFile) {
			d := deployment.NewDeployment(g.docker, remoteFile)
			d.State = deployment.Added
			deployments = append(deployments, d)
		}
	}

	// Ensure docker login if credentials are set
	if _, err = g.docker.LoginIfCredentialsSet(); err != nil {
		slog.Error("error logging in to docker registry", "err", err)
		return []*deployment.Deployment{}, err
	}

	// Stop removed deployments first.
	// When changedDirs is provided, only stop deployments in those dirs.
	for _, d := range deployments {
		if d.IsIgnored() || d.IsController() {
			continue
		}
		if d.State != deployment.Removed {
			continue
		}
		if !g.inScope(d, changedDirs, incremental) {
			continue
		}
		g.applyDeploymentChange(d, state)
	}

	// Pull Git changes.
	// IMPORTANT: Pull happens AFTER we record the list of changed dirs but
	// BEFORE we apply the new compose files — this is identical to the
	// original behaviour.
	if err := g.repo.Pull(); err != nil {
		slog.Error("error pulling changes", "err", err)
		return deployments, err
	}

	// Reload config for non-removed deployments (picks up new image tags etc.)
	for _, d := range deployments {
		if d.State != deployment.Removed {
			if err := d.LoadConfig(); err != nil {
				slog.Error("error loading deployment config", "file", d.Filepath, "err", err)
			}
		}
	}

	// If a deployment is in the changed dirs set but the compose-go hash didn't
	// change (e.g. the changed variable is not used in interpolation, such as a
	// raw image tag in .env that compose-go resolves to blank), force it to
	// Updated so docker compose up is always run for git-changed deployments.
	if incremental {
		for _, d := range deployments {
			if d.State == deployment.Unchanged && g.inScope(d, changedDirs, incremental) {
				slog.Debug("forcing re-deploy: git change or retry with hash unchanged",
					"file", d.Filepath)
				d.State = deployment.Updated
			}
		}
	}

	// Apply add/update/unchanged deployments.
	for _, d := range deployments {
		if d.IsIgnored() || d.IsController() || d.State == deployment.Removed {
			continue
		}
		if incremental && !g.inScope(d, changedDirs, incremental) {
			state.Unchanged++
			continue
		}
		g.applyDeploymentChange(d, state)
	}

	// Post-deployment bookkeeping
	for _, d := range deployments {
		if d.IsIgnored() {
			if d.State != deployment.Removed {
				state.Ignored++
				slog.Info("skipping deployment due to gitops ignore label", "file", d.Filepath)
			}
			continue
		}
		if d.IsController() {
			switch d.State {
			case deployment.Removed:
				slog.Error("cannot remove controller deployment", "file", d.Filepath)
				state.Failed++
			case deployment.Added:
				slog.Error("cannot add controller deployment", "file", d.Filepath)
				state.Failed++
			case deployment.Updated:
				slog.Error("update controller deployment is not implemented yet", "file", d.Filepath)
			}
		}
	}

	return deployments, nil
}

func (g *GitOps) CheckAndUpdate() {
	if g.isFirstCheck {
		defer func() { g.isFirstCheck = false }()
	}

	hasChanges, err := g.repo.HasChanges()
	if err != nil {
		g.metrics.TrackCheckStatus("error")
		slog.Error("error checking for git changes", "err", err)
		return
	}

	g.metrics.TrackCheckStatus("success")
	if hasChanges {
		slog.Info("git changes detected")
	} else if g.isFirstCheck {
		slog.Info("first run, ensuring all deployments are running")
	} else {
		slog.Info("no git changes detected")
	}

	newRetryDeployments := []*deployment.Deployment{}
	defer func() {
		g.retryDeployments = newRetryDeployments
		for _, d := range g.retryDeployments {
			slog.Info("scheduling deployment for retry", "file", d.Filepath, "err", d.Error)
		}
	}()

	if hasChanges || g.isFirstCheck {
		// First run: full reconcile (incremental=false).
		// Later polls: incremental; empty changedDirs means no in-scope
		// compose deployments changed (pull to advance HEAD, skip apply
		// unless a previous failure is queued for retry).
		var changedDirs []string
		incremental := false
		if hasChanges && !g.isFirstCheck {
			changedDirs, err = g.repo.ChangedDeploymentDirs()
			if err != nil {
				slog.Error("error computing changed deployment dirs", "err", err)
				changedDirs = nil
				incremental = false
			} else {
				incremental = true
				if len(changedDirs) > 0 {
					slog.Info("changed deployment directories", "dirs", changedDirs)
				}
			}
		}

		if incremental && len(changedDirs) == 0 && len(g.retryDeployments) == 0 {
			if err := g.repo.Pull(); err != nil {
				slog.Error("error pulling changes", "err", err)
				g.metrics.TrackCheckStatus("error")
				return
			}
			slog.Info("git changes outside watched deployments path, skipping apply")
			return
		}

		state := metrics.NewState()
		deployments, err := g.checkAndUpdateDeployments(state, changedDirs, incremental)
		if err != nil {
			slog.Error("error checking and updating deployments", "err", err)
			g.metrics.TrackCheckStatus("error")
			return
		}
		g.metrics.TrackState(state, true)

		for _, d := range deployments {
			if shouldRetry(d) {
				newRetryDeployments = append(newRetryDeployments, d)
			}
		}

		if state.HasChanges() {
			slog.Info("deployment changes applied")
		} else {
			slog.Info("no deployment changes necessary")
		}
	} else if len(g.retryDeployments) > 0 {
		slog.Info("retrying previously failed deployments",
			"count", len(g.retryDeployments))
		state := metrics.NewState()
		for _, d := range g.retryDeployments {
			g.applyDeploymentChange(d, state)
			if shouldRetry(d) {
				newRetryDeployments = append(newRetryDeployments, d)
			}
		}
		g.metrics.TrackState(state, false)
	}
}
