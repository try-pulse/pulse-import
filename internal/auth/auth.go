package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"gopkg.in/yaml.v3"
)

const (
	EnvAccessToken = "PULSE_ACCESS_TOKEN"
	EnvAPIKey      = "PULSE_API_KEY"
	EnvAPIURL      = "PULSE_API_URL"
	EnvWorkspace   = "PULSE_WORKSPACE_ID"
)

type Config struct {
	AccessToken string `yaml:"access_token,omitempty"`
	APIURL      string `yaml:"api_url,omitempty"`
	WorkspaceID string `yaml:"workspace_id,omitempty"`
}

var userConfigDir = os.UserConfigDir

func SetUserConfigDirForTest(fn func() (string, error)) {
	userConfigDir = fn
}

func ResetUserConfigDirForTest() {
	userConfigDir = os.UserConfigDir
}

func configPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pulse-import", "config.yaml"), nil
}

func Load() (*Config, error) {
	path, err := configPath()
	if err != nil {
		return &Config{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return &cfg, nil
}

func ConfigPathHint() string {
	path, err := configPath()
	if err != nil {
		return "~/.config/pulse-import/config.yaml"
	}
	return path
}

func Save(cfg *Config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func ResolveToken(cfg *Config, nonInteractive bool) (string, error) {
	if v := strings.TrimSpace(os.Getenv(EnvAccessToken)); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv(EnvAPIKey)); v != "" {
		return v, nil
	}
	if cfg != nil && strings.TrimSpace(cfg.AccessToken) != "" {
		return strings.TrimSpace(cfg.AccessToken), nil
	}
	if nonInteractive {
		return "", fmt.Errorf("no API token: set %s or %s", EnvAccessToken, EnvAPIKey)
	}

	var token string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Pulse API token").
				Description("Paste a JWT access token (same as the web app Authorization Bearer). Create a session via the Pulse app or pulse login.").
				EchoMode(huh.EchoModePassword).
				Value(&token).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("token is required")
					}
					return nil
				}),
		),
	)
	if err := form.Run(); err != nil {
		return "", err
	}
	token = strings.TrimSpace(token)
	if cfg != nil {
		cfg.AccessToken = token
		if err := Save(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save token to %s: %v\n", ConfigPathHint(), err)
		} else {
			fmt.Fprintf(os.Stderr, "saved token to %s\n", ConfigPathHint())
		}
	}
	return token, nil
}

func DefaultAPIURL() string {
	if v := strings.TrimSpace(os.Getenv(EnvAPIURL)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.trypulse.tech/api/v1"
}
