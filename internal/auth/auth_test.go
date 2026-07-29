package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/try-pulse/pulse-import/internal/auth"
)

func TestResolveToken_EnvPrecedence(t *testing.T) {
	t.Setenv(auth.EnvAccessToken, "")
	t.Setenv(auth.EnvAPIKey, "")

	tests := []struct {
		name    string
		access  string
		apiKey  string
		cfg     *auth.Config
		want    string
		wantErr bool
	}{
		{
			name:   "access token wins",
			access: " tok-a ",
			apiKey: "tok-b",
			cfg:    &auth.Config{AccessToken: "tok-cfg"},
			want:   "tok-a",
		},
		{
			name:   "api key when access empty",
			apiKey: "tok-b",
			cfg:    &auth.Config{AccessToken: "tok-cfg"},
			want:   "tok-b",
		},
		{
			name: "config when env empty",
			cfg:  &auth.Config{AccessToken: " tok-cfg "},
			want: "tok-cfg",
		},
		{
			name:    "noninteractive missing",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(auth.EnvAccessToken, tt.access)
			t.Setenv(auth.EnvAPIKey, tt.apiKey)
			got, err := auth.ResolveToken(tt.cfg, true)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
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

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	auth.SetUserConfigDirForTest(func() (string, error) { return dir, nil })
	t.Cleanup(auth.ResetUserConfigDirForTest)

	cfg := &auth.Config{
		AccessToken: "secret",
		APIURL:      "https://api.example.com/api/v1",
		WorkspaceID: "ws1",
	}
	if err := auth.Save(cfg); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pulse-import", "config.yaml")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		t.Fatalf("config should not be group/world writable: %v", perm)
	}

	loaded, err := auth.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "secret" || loaded.WorkspaceID != "ws1" {
		t.Fatalf("loaded %+v", loaded)
	}
}

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	auth.SetUserConfigDirForTest(func() (string, error) { return dir, nil })
	t.Cleanup(auth.ResetUserConfigDirForTest)

	cfg, err := auth.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessToken != "" {
		t.Fatalf("expected empty config, got %+v", cfg)
	}
}
