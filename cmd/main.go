package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/korbiniankuhn/gitops-compose/internal/config"
	"github.com/korbiniankuhn/gitops-compose/internal/docker"
	"github.com/korbiniankuhn/gitops-compose/internal/git"
	"github.com/korbiniankuhn/gitops-compose/internal/gitops"
	"github.com/korbiniankuhn/gitops-compose/internal/metrics"
)

func exitOnError(message string, err error) {
	if err != nil {
		slog.Error(message, "error", err)
		os.Exit(1)
	}
}

func enqueueCheck(check chan struct{}) {
	select {
	case check <- struct{}{}:
	default:
	}
}

func main() {
	code, stop := parseArgs(os.Args[1:], os.Stdout, os.Stderr)
	if stop {
		os.Exit(code)
	}

	// Default logger (will be overwritten during config load)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	c, err := config.Get()
	if err != nil {
		printConfigError(err)
		os.Exit(1)
	}

	switch c.LogFormat {
	case "text":
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.Level(c.LogLevel),
		})))
	case "json":
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.Level(c.LogLevel),
		})))
	default:
		slog.SetLogLoggerLevel(slog.Level(c.LogLevel))
	}

	deploymentRepoOptions := []git.DeploymentRepoOption{
		git.WithBranch(c.RepositoryBranch),
	}

	if c.DeploymentsPath != "" {
		deploymentRepoOptions = append(deploymentRepoOptions,
			git.WithDeploymentsPath(c.DeploymentsPath))
		slog.Info("scoping deployments to subdirectory", "path", c.DeploymentsPath)
	}

	if c.SSHEnabled() {
		slog.Info("SSH auth configured", "key", c.SSHKeyPath)
		deploymentRepoOptions = append(deploymentRepoOptions,
			git.WithSSH(c.SSHKeyPath, c.SSHKnownHostsPath))
	} else if c.RepositoryUsername != "" {
		deploymentRepoOptions = append(deploymentRepoOptions,
			git.WithAuth(c.RepositoryUsername, c.RepositoryPassword))
	} else {
		slog.Warn("no credentials set in repository origin")
	}

	r, err := git.NewDeploymentRepo(c.RepositoryPath, deploymentRepoOptions...)
	exitOnError("failed to create deployment repo", err)
	slog.Info("deployment repo initialised", "path", c.RepositoryPath)

	exitOnError("failed to verify git remote access", r.VerifyRemoteAccess())
	exitOnError("failed to verify git cli", r.VerifyGitCli())
	slog.Info("git remote access verified")

	d := docker.NewDocker(c.DockerRegistries)
	exitOnError("failed to verify docker socket connection", d.VerifySocketConnection())
	slog.Info("docker socket connection verified")

	if c.IsRunningInDocker {
		isDockerDesktop, err := d.IsDockerDesktop()
		exitOnError("failed to verify if docker is running in docker desktop", err)
		if isDockerDesktop {
			slog.Warn("docker is running in docker desktop (volume mounts might cause issues)")
		}
	}

	loggedIn, err := d.LoginIfCredentialsSet()
	exitOnError("failed to verify docker registry credentials", err)
	if loggedIn {
		slog.Info("docker registry credentials verified")
	}

	m := metrics.NewMetrics()
	if c.MetricsEnabled {
		http.Handle("/metrics", m.GetMetricsHandler())
		slog.Info("metrics enabled", "url", "/metrics")
	}

	g := gitops.NewGitOps(r, d, m)

	stopCh := make(chan struct{})
	check := make(chan struct{}, 1)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			case <-check:
				g.CheckAndUpdate()
			}
		}
	}()

	if c.WebhookEnabled {
		http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
			select {
			case check <- struct{}{}:
				slog.Info("triggered check via webhook")
			default:
				slog.Info("ignored webhook as channel is already full")
			}
			w.WriteHeader(http.StatusAccepted)
		})
		slog.Info("webhook enabled", "url", "/webhook")
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	slog.Info("health check endpoint", "url", "/health")

	s := http.Server{
		Addr: ":2112",
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("failed to start http server", "error", err)
			os.Exit(1)
		}
	}()
	slog.Info("http server started", "port", "2112")

	enqueueCheck(check)

	if c.CheckIntervalInSeconds > 0 {
		slog.Info("starting gitops repeated pull",
			"interval", fmt.Sprintf("%ds", c.CheckIntervalInSeconds))
		go func() {
			ticker := time.NewTicker(time.Duration(c.CheckIntervalInSeconds) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return
				case <-ticker.C:
					enqueueCheck(check)
				}
			}
		}()
	} else {
		slog.Info("skipping gitops repeated pull (check interval is negative)")
	}

	osSignal := make(chan os.Signal, 1)
	signal.Notify(osSignal, syscall.SIGINT, syscall.SIGTERM)

	<-osSignal
	slog.Info("received termination signal, shutting down")

	close(stopCh)

	ctx, cancel := context.WithTimeout(context.TODO(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		slog.Error("failed to shutdown http server", "error", err)
	}

	wg.Wait()
	slog.Info("gitops compose gracefully stopped")
}
