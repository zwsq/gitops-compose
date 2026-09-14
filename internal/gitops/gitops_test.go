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
