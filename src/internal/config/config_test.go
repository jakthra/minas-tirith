package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"minas-tirith/internal/configstore"
)

// writeWatchedConfig writes a declarative watched-repos config file into
// the test's temp dir and returns its path.
func writeWatchedConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "watched.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// setWatchedConfigEnv writes a single-repo watched config and points
// WATCHED_CONFIG at it, satisfying the listener config's requirement.
func setWatchedConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv(EnvWatchedConfig, writeWatchedConfig(t,
		"repos:\n  - project: BeanieGenie\n    secret: s3cret\n"))
}

// multiRepoConfig declares two repositories, each with its own secret.
const multiRepoConfig = `repos:
  - project: BeanieGenie
    secret: beanie-secret
  - project: Garden
    secret: garden-secret
`

func TestLoadFromEnv(t *testing.T) {
	t.Setenv(EnvListenAddr, "127.0.0.1:9000")
	t.Setenv(EnvWatchedConfig, writeWatchedConfig(t, multiRepoConfig))
	t.Setenv(EnvDedupeTTL, "2h")
	t.Setenv(EnvTemporalHostPort, "temporal.example.com:7233")
	t.Setenv(EnvTemporalNamespace, "production")
	t.Setenv(EnvTemporalTaskQueue, "agents")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("listen addr = %q", cfg.ListenAddr)
	}
	if cfg.DedupeTTL != 2*time.Hour {
		t.Errorf("dedupe ttl = %v", cfg.DedupeTTL)
	}
	wantTemporal := TemporalConfig{HostPort: "temporal.example.com:7233", Namespace: "production", TaskQueue: "agents"}
	if cfg.Temporal != wantTemporal {
		t.Errorf("temporal = %+v, want %+v", cfg.Temporal, wantTemporal)
	}

	// The store carries the declarative config: multiple repositories,
	// each with its own secret.
	repos, err := cfg.Store.Repositories()
	if err != nil {
		t.Fatalf("load watched repos: %v", err)
	}
	want := []configstore.Repository{
		{Project: "BeanieGenie", Secret: "beanie-secret"},
		{Project: "Garden", Secret: "garden-secret"},
	}
	if len(repos) != len(want) {
		t.Fatalf("watched repos = %#v, want %#v", repos, want)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Errorf("watched repos[%d] = %+v, want %+v", i, repos[i], want[i])
		}
	}
}

func TestLoadDefaults(t *testing.T) {
	setWatchedConfigEnv(t)
	t.Setenv(EnvListenAddr, "")
	t.Setenv(EnvDedupeTTL, "")
	t.Setenv(EnvTemporalHostPort, "")
	t.Setenv(EnvTemporalNamespace, "")
	t.Setenv(EnvTemporalTaskQueue, "")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Errorf("listen addr default = %q, want %s", cfg.ListenAddr, DefaultListenAddr)
	}
	if cfg.DedupeTTL != 24*time.Hour {
		t.Errorf("dedupe ttl default = %v, want 24h", cfg.DedupeTTL)
	}
	wantTemporal := TemporalConfig{
		HostPort:  DefaultTemporalHostPort,
		Namespace: DefaultTemporalNamespace,
		TaskQueue: DefaultTemporalTaskQueue,
	}
	if cfg.Temporal != wantTemporal {
		t.Errorf("temporal default = %+v, want %+v", cfg.Temporal, wantTemporal)
	}
}

func TestFlagsOverrideEnv(t *testing.T) {
	t.Setenv(EnvListenAddr, "127.0.0.1:9000")
	t.Setenv(EnvWatchedConfig, writeWatchedConfig(t,
		"repos:\n  - project: EnvRepo\n    secret: env-secret\n"))
	t.Setenv(EnvDedupeTTL, "2h")
	t.Setenv(EnvTemporalHostPort, "env-temporal:7233")

	cfg, err := Load([]string{
		"-listen", ":7070",
		"-watched-config", writeWatchedConfig(t,
			"repos:\n  - project: FlagRepo\n    secret: flag-secret\n"),
		"-dedupe-ttl", "30m",
		"-temporal-host-port", "flag-temporal:7233",
		"-temporal-namespace", "flag-ns",
		"-temporal-task-queue", "flag-queue",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenAddr != ":7070" {
		t.Errorf("listen addr = %q, want :7070", cfg.ListenAddr)
	}
	if cfg.DedupeTTL != 30*time.Minute {
		t.Errorf("dedupe ttl = %v, want 30m", cfg.DedupeTTL)
	}
	wantTemporal := TemporalConfig{HostPort: "flag-temporal:7233", Namespace: "flag-ns", TaskQueue: "flag-queue"}
	if cfg.Temporal != wantTemporal {
		t.Errorf("temporal = %+v, want %+v", cfg.Temporal, wantTemporal)
	}
	repos, err := cfg.Store.Repositories()
	if err != nil {
		t.Fatalf("load watched repos: %v", err)
	}
	want := []configstore.Repository{{Project: "FlagRepo", Secret: "flag-secret"}}
	if len(repos) != 1 || repos[0] != want[0] {
		t.Errorf("watched repos = %#v, want the flag file's %#v", repos, want)
	}
}

func TestLoadDefaultWatchedConfig(t *testing.T) {
	// With no flag and no env, the working directory's watched.yaml is
	// the default dev config — the listener starts bare for local
	// development.
	t.Chdir(t.TempDir())
	if err := os.WriteFile(DefaultWatchedConfig, []byte(
		"repos:\n  - project: BeanieGenie\n    secret: dev-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvWatchedConfig, "")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	repos, err := cfg.Store.Repositories()
	if err != nil {
		t.Fatalf("load watched repos: %v", err)
	}
	want := []configstore.Repository{{Project: "BeanieGenie", Secret: "dev-secret"}}
	if len(repos) != 1 || repos[0] != want[0] {
		t.Errorf("watched repos = %+v, want the default file's %+v", repos, want)
	}

	t.Run("env wins over the default", func(t *testing.T) {
		t.Setenv(EnvWatchedConfig, writeWatchedConfig(t,
			"repos:\n  - project: EnvRepo\n    secret: env-secret\n"))

		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		repos, err := cfg.Store.Repositories()
		if err != nil {
			t.Fatalf("load watched repos: %v", err)
		}
		if len(repos) != 1 || repos[0].Project != "EnvRepo" {
			t.Errorf("watched repos = %+v, want the env file's EnvRepo", repos)
		}
	})
}

func TestLoadMissingWatchedConfig(t *testing.T) {
	// Nothing set and no default file: the error names every option.
	t.Chdir(t.TempDir())
	t.Setenv(EnvWatchedConfig, "")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("expected error for missing watched config")
	}
	want := "watched config file is required: set -watched-config or WATCHED_CONFIG, or create watched.yaml in the working directory"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestLoadInvalidDedupeTTL(t *testing.T) {
	setWatchedConfigEnv(t)
	t.Setenv(EnvDedupeTTL, "not-a-duration")

	if _, err := Load(nil); err == nil {
		t.Fatal("expected error for invalid DEDUPE_TTL")
	}
}

func TestMain(m *testing.M) {
	// Config tests must not inherit a developer's env: temporal, forgejo,
	// or llm.
	os.Unsetenv(EnvTemporalHostPort)
	os.Unsetenv(EnvTemporalNamespace)
	os.Unsetenv(EnvTemporalTaskQueue)
	os.Unsetenv(EnvForgejoURL)
	os.Unsetenv(EnvForgejoToken)
	os.Unsetenv(EnvLLMBaseURL)
	os.Unsetenv(EnvLLMAPIKey)
	os.Unsetenv(EnvLLMModel)
	os.Exit(m.Run())
}
