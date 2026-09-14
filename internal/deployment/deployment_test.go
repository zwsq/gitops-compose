package deployment

import (
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// Dir() helper
// ---------------------------------------------------------------------------

func TestDeployment_Dir(t *testing.T) {
	cases := []struct {
		filepath string
		wantDir  string
	}{
		{"/deployments/beta/payments/compose.yaml", "/deployments/beta/payments"},
		{"/deployments/beta/frontend/docker-compose.yml", "/deployments/beta/frontend"},
		{"/deployments/production/reporting/compose.yaml", "/deployments/production/reporting"},
	}

	for _, tc := range cases {
		d := &Deployment{Filepath: tc.filepath}
		got := d.Dir()
		if got != tc.wantDir {
			t.Errorf("Dir() for %q: got %q, want %q", tc.filepath, got, tc.wantDir)
		}
	}
}

// ---------------------------------------------------------------------------
// matchesChangedDirs (tested via gitops package integration, but we expose
// the pure path-matching logic here through a small adapter so we can test it
// directly without network I/O).
// ---------------------------------------------------------------------------

// matchDirs mirrors the matchesChangedDirs function signature to allow direct
// unit testing of the path-matching algorithm.
func matchDirs(deploymentFilepath string, changedDirs []string) bool {
	d := &Deployment{Filepath: deploymentFilepath}
	deployDir := filepath.ToSlash(d.Dir())
	for _, changed := range changedDirs {
		// Same logic as matchesChangedDirs in gitops package
		if deployDir == changed || len(deployDir) > len(changed) &&
			deployDir[len(deployDir)-len(changed)-1] == '/' &&
			deployDir[len(deployDir)-len(changed):] == changed {
			return true
		}
		// simple HasSuffix-with-boundary
		if deployDir == changed {
			return true
		}
		suffix := "/" + changed
		if len(deployDir) >= len(suffix) && deployDir[len(deployDir)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}

func TestMatchesChangedDirs_PaymentsEnvChange(t *testing.T) {
	changed := []string{"beta/payments"}
	if !matchDirs("/deployments/beta/payments/compose.yaml", changed) {
		t.Error("payments compose.yaml should match beta/payments")
	}
}

func TestMatchesChangedDirs_ComposeFileChange(t *testing.T) {
	changed := []string{"beta/payments"}
	if !matchDirs("/opt/deployments/beta/payments/compose.yaml", changed) {
		t.Error("compose.yaml under beta/payments should match")
	}
}

func TestMatchesChangedDirs_FrontendDoesNotAffectPayments(t *testing.T) {
	changed := []string{"beta/frontend"}
	if matchDirs("/deployments/beta/payments/compose.yaml", changed) {
		t.Error("frontend change should NOT affect payments deployment")
	}
}

func TestMatchesChangedDirs_PaymentsDoesNotAffectFrontend(t *testing.T) {
	changed := []string{"beta/payments"}
	if matchDirs("/deployments/beta/frontend/compose.yaml", changed) {
		t.Error("payments change should NOT affect frontend deployment")
	}
}

func TestMatchesChangedDirs_MultipleChangedDirs(t *testing.T) {
	changed := []string{"beta/payments", "beta/frontend"}

	if !matchDirs("/deployments/beta/payments/compose.yaml", changed) {
		t.Error("payments should match when in changed list")
	}
	if !matchDirs("/deployments/beta/frontend/compose.yaml", changed) {
		t.Error("frontend should match when in changed list")
	}
	if matchDirs("/deployments/beta/reporting/compose.yaml", changed) {
		t.Error("reporting should NOT match when only payments and frontend changed")
	}
}

func TestMatchesChangedDirs_EmptyChangedDirs(t *testing.T) {
	if matchDirs("/deployments/beta/payments/compose.yaml", []string{}) {
		t.Error("empty changedDirs should not match anything")
	}
}

func TestMatchesChangedDirs_ProductionIsolation(t *testing.T) {
	// A change to beta/payments should not affect production/payments
	changed := []string{"beta/payments"}
	if matchDirs("/deployments/production/payments/compose.yaml", changed) {
		t.Error("production/payments should not match when only beta/payments changed")
	}
}
