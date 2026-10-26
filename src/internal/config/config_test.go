package config

import (
	"testing"
	"time"
)

func TestLoadFromEnv(t *testing.T) {
	t.Setenv(EnvListenAddr, "127.0.0.1:9000")
	t.Setenv(EnvSecret, "s3cret")
	t.Setenv(EnvWatchedRepos, "BeanieGenie, Garden,  ")
	t.Setenv(EnvDedupeTTL, "2h")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" {
		t.Errorf("listen addr = %q", cfg.ListenAddr)
	}
	if cfg.Secret != "s3cret" {
		t.Errorf("secret = %q", cfg.Secret)
	}
	if len(cfg.WatchedRepos) != 2 || cfg.WatchedRepos[0] != "BeanieGenie" || cfg.WatchedRepos[1] != "Garden" {
		t.Errorf("watched = %#v", cfg.WatchedRepos)
	}
	if cfg.DedupeTTL != 2*time.Hour {
		t.Errorf("dedupe ttl = %v", cfg.DedupeTTL)
	}
}

func TestLoadDefaults(t *testing.T) {
	t.Setenv(EnvListenAddr, "")
	t.Setenv(EnvSecret, "s3cret")
	t.Setenv(EnvWatchedRepos, "BeanieGenie")
	t.Setenv(EnvDedupeTTL, "")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenAddr != ":8080" {
		t.Errorf("listen addr default = %q, want :8080", cfg.ListenAddr)
	}
	if cfg.DedupeTTL != 24*time.Hour {
		t.Errorf("dedupe ttl default = %v, want 24h", cfg.DedupeTTL)
	}
}

func TestFlagsOverrideEnv(t *testing.T) {
	t.Setenv(EnvListenAddr, "127.0.0.1:9000")
	t.Setenv(EnvSecret, "env-secret")
	t.Setenv(EnvWatchedRepos, "EnvRepo")
	t.Setenv(EnvDedupeTTL, "2h")

	cfg, err := Load([]string{
		"-listen", ":7070",
		"-secret", "flag-secret",
		"-watched", "FlagRepo",
		"-dedupe-ttl", "30m",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ListenAddr != ":7070" || cfg.Secret != "flag-secret" {
		t.Errorf("flag overrides ignored: %+v", cfg)
	}
	if len(cfg.WatchedRepos) != 1 || cfg.WatchedRepos[0] != "FlagRepo" {
		t.Errorf("watched = %#v", cfg.WatchedRepos)
	}
	if cfg.DedupeTTL != 30*time.Minute {
		t.Errorf("dedupe ttl = %v, want 30m", cfg.DedupeTTL)
	}
}

func TestLoadReportsAllMissing(t *testing.T) {
	t.Setenv(EnvSecret, "")
	t.Setenv(EnvWatchedRepos, "")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("expected error for missing secret and watched repos")
	}
	for _, want := range []string{"webhook secret is required", "watched repositories are required"} {
		if !contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestLoadInvalidDedupeTTL(t *testing.T) {
	t.Setenv(EnvSecret, "s")
	t.Setenv(EnvWatchedRepos, "r")
	t.Setenv(EnvDedupeTTL, "not-a-duration")

	if _, err := Load(nil); err == nil {
		t.Fatal("expected error for invalid DEDUPE_TTL")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
