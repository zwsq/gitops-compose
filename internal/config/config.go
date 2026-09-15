package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/kelseyhightower/envconfig"
	"github.com/korbiniankuhn/gitops-compose/internal/docker"
)

type LogFormatDecoder string
type LogLevelDecoder slog.Level

type Config struct {
	// How often (in seconds) to poll Git for changes. Negative disables polling.
	CheckIntervalInSeconds int `default:"300" split_words:"true"`

	// Local path where the Git repository is (or will be) cloned.
	RepositoryPath string `required:"true" split_words:"true"`

	// Optional subdirectory inside the repository to watch. When set, only
	// changes under this path trigger deployments. Empty means watch the
	// entire repository.
	DeploymentsPath string `split_words:"true"`

	// Branch to track. Defaults to "main" for backward-compatibility.
	RepositoryBranch string `default:"main" split_words:"true"`

	// Optional HTTP basic-auth credentials (extracted from the remote URL at
	// startup for backward-compatibility; not used when SSH is configured).
	RepositoryUsername string `ignored:"true"`
	RepositoryPassword string `ignored:"true"`

	// SSH private key path (e.g. /ssh/id_ed25519). When set, SSH auth is used
	// instead of HTTP basic-auth.
	SSHKeyPath string `split_words:"true"`

	// Path to a known_hosts file used for SSH host verification.
	// Defaults to the standard ~/.ssh/known_hosts when empty.
	SSHKnownHostsPath string `split_words:"true"`

	WebhookEnabled    bool                    `default:"true" split_words:"true"`
	MetricsEnabled    bool                    `default:"true" split_words:"true"`
	DockerRegistries  DockerRegistriesDecoder `default:"[]" split_words:"true"`
	IsRunningInDocker bool                    `default:"false" split_words:"true"`
	LogFormat         LogFormatDecoder        `default:"text" split_words:"true"`
	LogLevel          LogLevelDecoder         `default:"info" split_words:"true"`
}

// SSHEnabled returns true when an SSH key path is configured.
func (c *Config) SSHEnabled() bool {
	return c.SSHKeyPath != ""
}

// getCredentialsFromRepository extracts HTTP basic-auth credentials embedded in
// the remote URL of an already-cloned repository (backward-compat path).
func getCredentialsFromRepository(path string) (string, string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", ""
	}

	// Read the git config file directly to avoid importing go-git here
	configPath := filepath.Join(path, ".git", "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", ""
	}

	// Look for the remote "origin" URL line
	lines := strings.Split(string(data), "\n")
	inOrigin := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == `[remote "origin"]` {
			inOrigin = true
			continue
		}
		if inOrigin && strings.HasPrefix(trimmed, "[") {
			break
		}
		if inOrigin && strings.HasPrefix(trimmed, "url = ") {
			rawURL := strings.TrimPrefix(trimmed, "url = ")
			// Parse user:pass@host style URLs only (HTTP/HTTPS)
			if strings.Contains(rawURL, "@") && (strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://")) {
				// e.g. https://user:pass@host/…
				without := strings.TrimPrefix(strings.TrimPrefix(rawURL, "https://"), "http://")
				atIdx := strings.Index(without, "@")
				if atIdx < 0 {
					break
				}
				userInfo := without[:atIdx]
				colonIdx := strings.Index(userInfo, ":")
				if colonIdx < 0 {
					return userInfo, ""
				}
				return userInfo[:colonIdx], userInfo[colonIdx+1:]
			}
			break
		}
	}
	return "", ""
}

type DockerRegistriesDecoder []docker.DockerRegistryCredentials

func (d *DockerRegistriesDecoder) Decode(value string) error {
	var registries []docker.DockerRegistryCredentials
	if err := json.Unmarshal([]byte(value), &registries); err != nil {
		return err
	}
	*d = DockerRegistriesDecoder(registries)
	return nil
}

func (f *LogFormatDecoder) UnmarshalText(text []byte) error {
	value := strings.ToLower(string(text))
	switch value {
	case "json", "text", "console":
		*f = LogFormatDecoder(value)
		return nil
	default:
		return fmt.Errorf("invalid log format: %s", value)
	}
}

func (l *LogLevelDecoder) UnmarshalText(text []byte) error {
	value := strings.ToLower(string(text))
	switch value {
	case "debug":
		*l = LogLevelDecoder(slog.LevelDebug)
	case "info":
		*l = LogLevelDecoder(slog.LevelInfo)
	case "warn":
		*l = LogLevelDecoder(slog.LevelWarn)
	case "error":
		*l = LogLevelDecoder(slog.LevelError)
	default:
		return fmt.Errorf("invalid log level: %s", value)
	}
	return nil
}

func Get() (*Config, error) {
	godotenv.Load()

	var config Config
	if err := envconfig.Process("", &config); err != nil {
		return nil, err
	}

	if config.IsRunningInDocker {
		if !filepath.IsAbs(config.RepositoryPath) {
			return nil, fmt.Errorf("repository path must be absolute when running in Docker, got: %s", config.RepositoryPath)
		}
	}

	// Only extract HTTP credentials when SSH is not configured.
	if !config.SSHEnabled() {
		config.RepositoryUsername, config.RepositoryPassword = getCredentialsFromRepository(config.RepositoryPath)
	}

	return &config, nil
}
