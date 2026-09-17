package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// version is overridden at release time via -ldflags "-X main.version=..."
var version = "dev"

func usage() string {
	return `gitops-compose is a GitOps agent for single-node Docker Compose deployments.

It polls a Git repository, maps changed files to Compose directories, and
runs docker compose up for the affected stacks.

Usage:
  gitops-compose [flags]

Flags:
  -h, --help       Show this help
  -v, --version    Print version

Configuration is via environment variables (or a .env file in the working
directory). Example systemd EnvironmentFile: /etc/gitops-compose.env

Required:
  REPOSITORY_PATH              Absolute path to the cloned Git repository

Optional:
  REPOSITORY_BRANCH            Branch to track (default: main)
  DEPLOYMENTS_PATH             Subdirectory to watch recursively; files
                               outside this path are ignored
  SSH_KEY_PATH                 SSH private key (enables SSH auth)
  SSH_KNOWN_HOSTS_PATH         known_hosts file for SSH host verification
  CHECK_INTERVAL_IN_SECONDS    Poll interval; -1 disables polling (default: 300)
  DOCKER_REGISTRIES            JSON array of {url,username,password}
  WEBHOOK_ENABLED              Enable POST /webhook (default: true)
  METRICS_ENABLED              Enable GET /metrics (default: true)
  LOG_FORMAT                   text, json, or console (default: text)
  LOG_LEVEL                    debug, info, warn, or error (default: info)

HTTP server (port 2112):
  GET  /health     Liveness probe
  GET  /metrics    Prometheus metrics
  POST /webhook    Trigger an immediate check

When SSH_KEY_PATH is set, SSH is used and HTTP credentials in the remote
URL are ignored. Host key verification is always enabled.
`
}

func parseArgs(args []string, stdout, stderr io.Writer) (exitCode int, stop bool) {
	fs := flag.NewFlagSet("gitops-compose", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var showHelp, showVersion bool
	fs.BoolVar(&showHelp, "help", false, "Show this help")
	fs.BoolVar(&showHelp, "h", false, "Show this help")
	fs.BoolVar(&showVersion, "version", false, "Print version")
	fs.BoolVar(&showVersion, "v", false, "Print version")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Fprint(stdout, usage())
			return 0, true
		}
		fmt.Fprintf(stderr, "error: %v\n\n", err)
		fmt.Fprint(stderr, usage())
		return 2, true
	}
	if showHelp {
		fmt.Fprint(stdout, usage())
		return 0, true
	}
	if showVersion {
		fmt.Fprintln(stdout, version)
		return 0, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "error: unexpected argument %q\n\n", fs.Arg(0))
		fmt.Fprint(stderr, usage())
		return 2, true
	}
	return 0, false
}

func printConfigError(err error) {
	fmt.Fprintf(os.Stderr, "error: failed to load config: %v\n\n", err)
	fmt.Fprint(os.Stderr, usage())
}
