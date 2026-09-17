package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUsageMentionsRequiredAndScopedPath(t *testing.T) {
	text := usage()
	for _, want := range []string{
		"REPOSITORY_PATH",
		"DEPLOYMENTS_PATH",
		"--help",
		"--version",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("usage() missing %q", want)
		}
	}
}

func TestParseArgsHelp(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code, stop := parseArgs([]string{"--help"}, stdout, stderr)
	if code != 0 || !stop {
		t.Fatalf("expected exit 0 stop=true, got code=%d stop=%v", code, stop)
	}
	if !strings.Contains(stdout.String(), "REPOSITORY_PATH") {
		t.Errorf("help stdout missing usage, got %q", stdout.String())
	}
}

func TestParseArgsVersion(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code, stop := parseArgs([]string{"-v"}, stdout, stderr)
	if code != 0 || !stop {
		t.Fatalf("expected exit 0 stop=true, got code=%d stop=%v", code, stop)
	}
	if strings.TrimSpace(stdout.String()) != version {
		t.Errorf("version = %q, want %q", stdout.String(), version)
	}
}

func TestParseArgsUnknown(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code, stop := parseArgs([]string{"--nope"}, stdout, stderr)
	if code != 2 || !stop {
		t.Fatalf("expected exit 2 stop=true, got code=%d stop=%v", code, stop)
	}
	if !strings.Contains(stderr.String(), "DEPLOYMENTS_PATH") {
		t.Errorf("unknown-flag error should include usage, got %q", stderr.String())
	}
}

func TestParseArgsUnexpectedPositional(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	code, stop := parseArgs([]string{"extra"}, stdout, stderr)
	if code != 2 || !stop {
		t.Fatalf("expected exit 2 stop=true, got code=%d stop=%v", code, stop)
	}
}
