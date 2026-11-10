package config

import (
	"strings"
	"testing"
)

// clearInstallEnv clears every variable the installer config reads.
func clearInstallEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{EnvForgejoURL, EnvListenerURL, EnvWatchedConfig, EnvWebhookEvents} {
		t.Setenv(name, "")
	}
}

func TestLoadInstallDefaults(t *testing.T) {
	clearInstallEnv(t)

	cfg, err := LoadInstall(InstallFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ForgejoURL != DefaultForgejoURL {
		t.Errorf("forgejo url = %q, want %q", cfg.ForgejoURL, DefaultForgejoURL)
	}
	if cfg.ListenerURL != DefaultListenerURL {
		t.Errorf("listener url = %q, want %q", cfg.ListenerURL, DefaultListenerURL)
	}
	if cfg.WatchedConfig != DefaultWatchedConfig {
		t.Errorf("watched config = %q, want %q", cfg.WatchedConfig, DefaultWatchedConfig)
	}
	if strings.Join(cfg.WebhookEvents, ",") != strings.Join(DefaultWebhookEvents, ",") {
		t.Errorf("events = %v, want the defaults", cfg.WebhookEvents)
	}
}

func TestLoadInstallEnv(t *testing.T) {
	clearInstallEnv(t)
	t.Setenv(EnvForgejoURL, "https://forgejo.example.com")
	t.Setenv(EnvListenerURL, "https://listener.example.com")
	t.Setenv(EnvWatchedConfig, "custom.yaml")
	t.Setenv(EnvWebhookEvents, "push, issue_comment ,,")

	cfg, err := LoadInstall(InstallFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ForgejoURL != "https://forgejo.example.com" ||
		cfg.ListenerURL != "https://listener.example.com" ||
		cfg.WatchedConfig != "custom.yaml" {
		t.Errorf("cfg = %+v", *cfg)
	}
	if strings.Join(cfg.WebhookEvents, ",") != "push,issue_comment" {
		t.Errorf("events = %v, want push,issue_comment", cfg.WebhookEvents)
	}
}

func TestLoadInstallFlagsWinOverEnv(t *testing.T) {
	clearInstallEnv(t)
	t.Setenv(EnvForgejoURL, "https://env:3000")
	t.Setenv(EnvListenerURL, "https://env-listener")
	t.Setenv(EnvWatchedConfig, "env.yaml")
	t.Setenv(EnvWebhookEvents, "push")

	cfg, err := LoadInstall(InstallFlags{
		ForgejoURL:    "http://flag:3000",
		ListenerURL:   "http://flag-listener",
		WatchedConfig: "flag.yaml",
		WebhookEvents: "issue_comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ForgejoURL != "http://flag:3000" || cfg.ListenerURL != "http://flag-listener" ||
		cfg.WatchedConfig != "flag.yaml" || strings.Join(cfg.WebhookEvents, ",") != "issue_comment" {
		t.Errorf("cfg = %+v", *cfg)
	}
}

func TestLoadInstallDotEnv(t *testing.T) {
	writeDotEnv(t, "INSTALL_LISTENER_URL=https://dotenv-listener\nWATCHED_CONFIG=dotenv.yaml\n")

	cfg, err := LoadInstall(InstallFlags{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenerURL != "https://dotenv-listener" || cfg.WatchedConfig != "dotenv.yaml" {
		t.Errorf("cfg = %+v", *cfg)
	}
}

func TestLoadInstallRejectsInvalidURLs(t *testing.T) {
	tests := []struct {
		name  string
		flags InstallFlags
		want  string
	}{
		{name: "relative listener url", flags: InstallFlags{ListenerURL: "localhost:9797"}, want: "invalid listener url"},
		{name: "non-http forgejo url", flags: InstallFlags{ForgejoURL: "ftp://forgejo"}, want: "invalid forgejo url"},
		{name: "empty events", flags: InstallFlags{WebhookEvents: " , "}, want: "webhook events list is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearInstallEnv(t)
			_, err := LoadInstall(tt.flags)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}
