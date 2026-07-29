package auth_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/try-pulse/pulse-import/internal/auth"
)

func TestAccessToken(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, " env-access ")
	if got := auth.AccessToken(); got != "env-access" {
		t.Fatalf("access token = %q", got)
	}
	t.Setenv(auth.EnvAccessToken, "")
	if got := auth.AccessToken(); got != "" {
		t.Fatalf("missing token = %q", got)
	}
}

func TestDefaultAPIURL(t *testing.T) {
	t.Setenv(auth.EnvAPIURL, "")
	if got := auth.DefaultAPIURL(); got != "https://api.trypulse.tech/api/v1" {
		t.Fatalf("default = %q", got)
	}
	t.Setenv(auth.EnvAPIURL, "https://api.example.com/api/v1/")
	if got := auth.DefaultAPIURL(); got != "https://api.example.com/api/v1" {
		t.Fatalf("override = %q", got)
	}
}

func TestSaveLoadRoundTripHasNoSecret(t *testing.T) {
	dir := t.TempDir()
	auth.SetUserConfigDirForTest(func() (string, error) { return dir, nil })
	t.Cleanup(auth.ResetUserConfigDirForTest)

	cfg := &auth.Config{APIURL: "https://api.example.com/api/v1", WorkspaceID: "ws1"}
	if err := auth.Save(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pulse-import", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "token") {
		t.Fatalf("config contains token field: %s", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}

	loaded, err := auth.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIURL != cfg.APIURL || loaded.WorkspaceID != "ws1" {
		t.Fatalf("loaded %+v", loaded)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	auth.SetUserConfigDirForTest(func() (string, error) { return dir, nil })
	t.Cleanup(auth.ResetUserConfigDirForTest)
	configDir := filepath.Join(dir, "pulse-import")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("access_token: forbidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Load(); err == nil || !strings.Contains(err.Error(), "field access_token") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadRejectsMultipleDocuments(t *testing.T) {
	dir := t.TempDir()
	auth.SetUserConfigDirForTest(func() (string, error) { return dir, nil })
	t.Cleanup(auth.ResetUserConfigDirForTest)
	configDir := filepath.Join(dir, "pulse-import")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("workspace_id: ws1\n---\nworkspace_id: ws2\n")
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Load(); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	auth.SetUserConfigDirForTest(func() (string, error) { return dir, nil })
	t.Cleanup(auth.ResetUserConfigDirForTest)
	cfg, err := auth.Load()
	if err != nil || cfg.WorkspaceID != "" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}
