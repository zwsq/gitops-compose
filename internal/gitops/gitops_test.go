package gitops

import (
	"testing"

	"github.com/korbiniankuhn/gitops-compose/internal/deployment"
	"github.com/korbiniankuhn/gitops-compose/internal/metrics"
)

// ---------------------------------------------------------------------------
// matchesChangedDirs unit tests
// ---------------------------------------------------------------------------

func TestMatchesChangedDirs_PaymentsEnvChange(t *testing.T) {
	d := &deployment.Deployment{}
	// Use a synthesised Deployment via the exported Filepath field.
	// We call matchesChangedDirs directly (same package).
	d2 := deploymentWithPath("/deployments/beta/payments/compose.yaml")
	if !matchesChangedDirs(d2, []string{"beta/payments"}) {
		t.Error("payments compose.yaml should match beta/payments")
	}
	_ = d
}

func TestMatchesChangedDirs_FrontendDoesNotAffectPayments(t *testing.T) {
	d := deploymentWithPath("/deployments/beta/payments/compose.yaml")
	if matchesChangedDirs(d, []string{"beta/frontend"}) {
		t.Error("frontend change should NOT affect payments deployment")
	}
}

func TestMatchesChangedDirs_ProductionNotAffectedByBeta(t *testing.T) {
	d := deploymentWithPath("/deployments/production/payments/compose.yaml")
	if matchesChangedDirs(d, []string{"beta/payments"}) {
		t.Error("production/payments should not match when only beta/payments changed")
	}
}

func TestMatchesChangedDirs_MultipleChangedDirs(t *testing.T) {
	payments := deploymentWithPath("/deployments/beta/payments/compose.yaml")
	frontend := deploymentWithPath("/deployments/beta/frontend/compose.yaml")
	reporting := deploymentWithPath("/deployments/beta/reporting/compose.yaml")

	changed := []string{"beta/payments", "beta/frontend"}

	if !matchesChangedDirs(payments, changed) {
		t.Error("payments should match")
	}
	if !matchesChangedDirs(frontend, changed) {
		t.Error("frontend should match")
	}
	if matchesChangedDirs(reporting, changed) {
		t.Error("reporting should not match")
	}
}

func TestMatchesChangedDirs_EmptyChangedDirs(t *testing.T) {
	d := deploymentWithPath("/deployments/beta/payments/compose.yaml")
	if matchesChangedDirs(d, []string{}) {
		t.Error("empty changedDirs should not match")
	}
}

// deploymentWithPath creates a minimal *deployment.Deployment with a given
// Filepath for testing matchesChangedDirs without touching Docker or git.
func deploymentWithPath(fp string) *deployment.Deployment {
	d := &deployment.Deployment{}
	d.Filepath = fp
	return d
}

func TestShouldRetry(t *testing.T) {
	ok := &deployment.Deployment{Error: deployment.ErrImagePullBackoff}
	if !shouldRetry(ok) {
		t.Error("image pull backoff should be retried")
	}
	failed := &deployment.Deployment{Error: errTest}
	if !shouldRetry(failed) {
		t.Error("apply failures should be retried")
	}
	invalid := &deployment.Deployment{Error: deployment.ErrInvalidComposeFile}
	if shouldRetry(invalid) {
		t.Error("invalid compose files should not be retried")
	}
	clean := &deployment.Deployment{}
	if shouldRetry(clean) {
		t.Error("successful apply should not be retried")
	}
}

var errTest = errString("compose up failed")

type errString string

func (e errString) Error() string { return string(e) }

func TestInScope_IncludesRetriesWhenIncremental(t *testing.T) {
	g := &GitOps{
		retryDeployments: []*deployment.Deployment{
			deploymentWithPath("/deployments/beta/payments/compose.yaml"),
		},
	}
	payments := deploymentWithPath("/deployments/beta/payments/compose.yaml")
	frontend := deploymentWithPath("/deployments/beta/frontend/compose.yaml")

	if !g.inScope(payments, []string{"beta/frontend"}, true) {
		t.Error("previously failed payments should stay in scope as a retry")
	}
	if !g.inScope(frontend, []string{"beta/frontend"}, true) {
		t.Error("changed frontend should be in scope")
	}
	reporting := deploymentWithPath("/deployments/beta/reporting/compose.yaml")
	if g.inScope(reporting, []string{"beta/frontend"}, true) {
		t.Error("unrelated reporting should not be in scope")
	}
	if !g.inScope(reporting, nil, false) {
		t.Error("full reconcile should include every deployment")
	}
}

// ---------------------------------------------------------------------------
// DeploymentState tracking
// ---------------------------------------------------------------------------

func TestDeploymentState_HasErrors(t *testing.T) {
	s := metrics.NewState()
	if s.HasErrors() {
		t.Error("new state should not have errors")
	}
	s.Failed = 1
	if !s.HasErrors() {
		t.Error("state with Failed=1 should have errors")
	}
}

func TestDeploymentState_HasChanges(t *testing.T) {
	s := metrics.NewState()
	if s.HasChanges() {
		t.Error("new state should not have changes")
	}
	s.Started = 1
	if !s.HasChanges() {
		t.Error("state with Started=1 should have changes")
	}
}

func TestDeploymentState_CountRunning(t *testing.T) {
	s := &metrics.DeploymentState{
		Unchanged: 2,
		Started:   1,
		Updated:   1,
		Stopped:   1,
		Failed:    1,
	}
	if s.CountRunning() != 4 {
		t.Errorf("expected CountRunning()=4, got %d", s.CountRunning())
	}
}
