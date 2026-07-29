package auth

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	EnvAccessToken = "PULSE_ACCESS_TOKEN"
	EnvAPIURL      = "PULSE_API_URL"
	EnvWorkspace   = "PULSE_WORKSPACE_ID"
)

type Config struct {
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
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse config: multiple YAML documents are not allowed")
		}
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := secureExistingPath(path); err != nil {
		return nil, err
	}
	cfg.APIURL = strings.TrimSpace(cfg.APIURL)
	cfg.WorkspaceID = strings.TrimSpace(cfg.WorkspaceID)
	return &cfg, nil
}

func secureExistingPath(path string) error {
	// #nosec G302 -- a private directory needs owner execute permission.
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("secure config directory: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure config file: %w", err)
	}
	return nil
}

func ConfigPathHint() string {
	path, err := configPath()
	if err != nil {
		return "~/.config/pulse-import/config.yaml"
	}
	return path
}

func Save(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// #nosec G302 -- a private directory needs owner execute permission.
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}

	data, err := yaml.Marshal(Config{
		APIURL:      strings.TrimSpace(cfg.APIURL),
		WorkspaceID: strings.TrimSpace(cfg.WorkspaceID),
	})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Chmod(path, 0o600)
}

func AccessToken() string {
	return strings.TrimSpace(os.Getenv(EnvAccessToken))
}

func DefaultAPIURL() string {
	if v := strings.TrimSpace(os.Getenv(EnvAPIURL)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.trypulse.tech/api/v1"
}
