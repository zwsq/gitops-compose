package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// FindComposeFile
// ---------------------------------------------------------------------------

func TestFindComposeFile_PreferComposeYaml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "compose.yaml", "services: {}")
	writeFile(t, dir, "docker-compose.yml", "services: {}")

	got, err := FindComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "compose.yaml" {
		t.Errorf("expected compose.yaml to be preferred, got %s", got)
	}
}

func TestFindComposeFile_FallbackDockerComposeYml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "docker-compose.yml", "services: {}")

	got, err := FindComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "docker-compose.yml" {
		t.Errorf("expected docker-compose.yml as fallback, got %s", got)
	}
}

func TestFindComposeFile_OnlyComposeYaml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "compose.yaml", "services: {}")

	got, err := FindComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "compose.yaml" {
		t.Errorf("expected compose.yaml, got %s", got)
	}
}

func TestFindComposeFile_DockerComposeYaml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "docker-compose.yaml", "services: {}")

	got, err := FindComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "docker-compose.yaml" {
		t.Errorf("expected docker-compose.yaml, got %s", got)
	}
}

func TestFindComposeFile_ComposeYml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "compose.yml", "services: {}")

	got, err := FindComposeFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != "compose.yml" {
		t.Errorf("expected compose.yml, got %s", got)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

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
