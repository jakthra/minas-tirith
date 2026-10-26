// Package config loads the listener configuration.
//
// Configuration comes from command-line flags layered over environment
// variables: flags win, then environment, then defaults. Nothing requires a
// code change to reconfigure — run the binary with different flags or env.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// Environment variables recognized by the listener.
const (
	EnvListenAddr   = "LISTEN_ADDR"    // address to listen on, default ":8080"
	EnvSecret       = "WEBHOOK_SECRET" // shared webhook secret (required)
	EnvWatchedRepos = "WATCHED_REPOS"  // comma-separated watched projects (required)
	EnvDedupeTTL    = "DEDUPE_TTL"     // duplicate-delivery window, default 24h
)

// Config is the listener configuration.
type Config struct {
	// ListenAddr is the address the webhook endpoint listens on, e.g.
	// ":8080". This is the endpoint providers post their webhooks to.
	ListenAddr string
	// Secret is the shared secret used to authenticate webhook deliveries.
	Secret string
	// WatchedRepos lists the project names whose webhook deliveries are
	// accepted. A project is addressed by name in the webhook route, so
	// the repository root/BeanieGenie is watched as "BeanieGenie".
	WatchedRepos []string
	// DedupeTTL is how long event identities are remembered so duplicate
	// deliveries are not dispatched twice.
	DedupeTTL time.Duration
}

// Load builds the configuration from args and the environment. It returns an
// error listing every problem found, so misconfiguration is reported in one
// go.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("listener", flag.ContinueOnError)
	listen := fs.String("listen", "", "address to listen on (env LISTEN_ADDR, default \":8080\")")
	secret := fs.String("secret", "", "shared webhook secret (env WEBHOOK_SECRET)")
	watched := fs.String("watched", "", "comma-separated watched projects (env WATCHED_REPOS)")
	dedupeTTL := fs.Duration("dedupe-ttl", 0, "duplicate-delivery window, e.g. 24h (env DEDUPE_TTL)")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	cfg := &Config{
		ListenAddr: firstNonEmpty(*listen, os.Getenv(EnvListenAddr)),
		Secret:     firstNonEmpty(*secret, os.Getenv(EnvSecret)),
		DedupeTTL:  *dedupeTTL,
	}
	if cfg.DedupeTTL == 0 {
		if v := os.Getenv(EnvDedupeTTL); v != "" {
			ttl, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("invalid %s: %w", EnvDedupeTTL, err)
			}
			cfg.DedupeTTL = ttl
		}
	}
	cfg.WatchedRepos = firstNonEmptyList(*watched, os.Getenv(EnvWatchedRepos))

	var errs []error
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.Secret == "" {
		errs = append(errs, errors.New("webhook secret is required: set -secret or WEBHOOK_SECRET"))
	}
	if len(cfg.WatchedRepos) == 0 {
		errs = append(errs, errors.New("watched repositories are required: set -watched or WATCHED_REPOS"))
	}
	if cfg.DedupeTTL <= 0 {
		cfg.DedupeTTL = 24 * time.Hour
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// firstNonEmptyList returns the first argument that lists at least one
// entry. Entries are trimmed and blank entries dropped.
func firstNonEmptyList(values ...string) []string {
	for _, v := range values {
		if v == "" {
			continue
		}
		var out []string
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}
