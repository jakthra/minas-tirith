// Package config loads the listener and worker configuration.
//
// Runtime settings come from command-line flags layered over the
// environment: flags win, then shell environment variables, then the
// working directory's .env file (development values; the committed
// .env.example documents every variable), then defaults. The watched
// repositories and their webhook secrets do not: they come from the
// declarative config store (internal/configstore), a YAML file today,
// so a deployment declares them once instead of flagging or exporting
// them — the shape a Kubernetes ConfigMap or, later, a CRD-backed store
// serves.
package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"minas-tirith/internal/configstore"
)

// Environment variables recognized by the listener.
const (
	EnvListenAddr        = "LISTEN_ADDR"         // address to listen on (default ":9797")
	EnvWatchedConfig     = "WATCHED_CONFIG"      // path to the declarative watched-repos config, default watched.yaml
	EnvDedupeTTL         = "DEDUPE_TTL"          // duplicate-delivery window, default 24h
	EnvTemporalHostPort  = "TEMPORAL_HOST_PORT"  // Temporal frontend gRPC address (default "localhost:7233")
	EnvTemporalNamespace = "TEMPORAL_NAMESPACE"  // Temporal namespace (default "default")
	EnvTemporalTaskQueue = "TEMPORAL_TASK_QUEUE" // base task queue for agent workers (default "minas-tirith")
)

// DefaultWatchedConfig is the declarative watched-repos config loaded
// when neither WATCHED_CONFIG nor -watched-config is set and the file
// exists in the working directory. The repository ships it (src/watched.yaml)
// as the local-development default.
const DefaultWatchedConfig = "watched.yaml"

// DefaultListenAddr is the address the listener's webhook endpoint
// listens on when neither LISTEN_ADDR nor -listen is set: an uncommon
// port, clear of common service ports (8080, Forgejo's 3000, Temporal's
// 7233).
const DefaultListenAddr = ":9797"

// Temporal defaults shared with the worker config.
const (
	DefaultTemporalHostPort  = "localhost:7233" // local dev server (temporal server start-dev)
	DefaultTemporalNamespace = "default"
	DefaultTemporalTaskQueue = "minas-tirith"
)

// TemporalConfig is the Temporal connection the listener uses to start
// workflows. The worker binary runs the Temporal workers that execute
// them.
type TemporalConfig struct {
	// HostPort is the Temporal frontend gRPC address.
	HostPort string
	// Namespace is the Temporal namespace workflows run in.
	Namespace string
	// TaskQueue is the base task queue. Each agent template polls its
	// own queue derived from it: TaskQueue + "-" + template name.
	TaskQueue string
}

// Config is the listener configuration.
type Config struct {
	// ListenAddr is the address the webhook endpoint listens on, e.g.
	// ":9797". This is the endpoint providers post their webhooks to.
	ListenAddr string
	// Store loads the watched repositories and their webhook secrets
	// from the declarative config source. Each watched repository has
	// its own secret.
	Store configstore.Store
	// DedupeTTL is how long event identities are remembered so duplicate
	// deliveries are not dispatched twice.
	DedupeTTL time.Duration
	// Temporal is the connection the listener uses to start workflows
	// for dispatched events. The worker binary executes them.
	Temporal TemporalConfig
}

// listenerFlags holds the listener's flag values during parsing.
type listenerFlags struct {
	listen        *string
	watchedConfig *string
	dedupeTTL     *time.Duration
	temporalHost  *string
	temporalNS    *string
	temporalQueue *string
}

// registerListenerFlags adds the listener's flags to fs.
func registerListenerFlags(fs *flag.FlagSet) *listenerFlags {
	return &listenerFlags{
		listen:        fs.String("listen", "", fmt.Sprintf("address to listen on (env LISTEN_ADDR, default %q)", DefaultListenAddr)),
		watchedConfig: fs.String("watched-config", "", "path to the declarative watched-repos config file, one repo with its webhook secret per entry (env WATCHED_CONFIG, default \"watched.yaml\" in the working directory)"),
		dedupeTTL:     fs.Duration("dedupe-ttl", 0, "duplicate-delivery window, e.g. 24h (env DEDUPE_TTL)"),
		temporalHost:  fs.String("temporal-host-port", "", "Temporal frontend gRPC address to start workflows on (env TEMPORAL_HOST_PORT, default \"localhost:7233\")"),
		temporalNS:    fs.String("temporal-namespace", "", "Temporal namespace to start workflows in (env TEMPORAL_NAMESPACE, default \"default\")"),
		temporalQueue: fs.String("temporal-task-queue", "", "base task queue the agent workers poll (env TEMPORAL_TASK_QUEUE, default \"minas-tirith\")"),
	}
}

// resolve layers flag values over the environment (the shell first,
// then the .env file) over defaults and validates the result. It
// returns an error listing every problem found, so misconfiguration is
// reported in one go.
func (lf *listenerFlags) resolve(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		ListenAddr: firstNonEmpty(*lf.listen, getenv(EnvListenAddr)),
		DedupeTTL:  *lf.dedupeTTL,
		Temporal: TemporalConfig{
			HostPort:  firstNonEmpty(*lf.temporalHost, getenv(EnvTemporalHostPort), DefaultTemporalHostPort),
			Namespace: firstNonEmpty(*lf.temporalNS, getenv(EnvTemporalNamespace), DefaultTemporalNamespace),
			TaskQueue: firstNonEmpty(*lf.temporalQueue, getenv(EnvTemporalTaskQueue), DefaultTemporalTaskQueue),
		},
	}
	if cfg.DedupeTTL == 0 {
		if v := getenv(EnvDedupeTTL); v != "" {
			ttl, err := time.ParseDuration(v)
			if err != nil {
				return nil, fmt.Errorf("invalid %s: %w", EnvDedupeTTL, err)
			}
			cfg.DedupeTTL = ttl
		}
	}
	var errs []error
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	// Layering: -watched-config wins, then WATCHED_CONFIG (shell or
	// .env), then the working directory's default dev config when one
	// exists.
	if path := firstNonEmpty(*lf.watchedConfig, getenv(EnvWatchedConfig)); path != "" {
		cfg.Store = configstore.NewFile(path)
	} else if fileExists(DefaultWatchedConfig) {
		cfg.Store = configstore.NewFile(DefaultWatchedConfig)
	} else {
		errs = append(errs, fmt.Errorf(
			"watched config file is required: set -watched-config or %s, or create %s in the working directory",
			EnvWatchedConfig, DefaultWatchedConfig))
	}
	if cfg.DedupeTTL <= 0 {
		cfg.DedupeTTL = 24 * time.Hour
	}
	if cfg.Temporal.HostPort == "" {
		errs = append(errs, fmt.Errorf("temporal host:port is required: set -temporal-host-port or %s", EnvTemporalHostPort))
	}
	if cfg.Temporal.Namespace == "" {
		errs = append(errs, fmt.Errorf("temporal namespace is required: set -temporal-namespace or %s", EnvTemporalNamespace))
	}
	if cfg.Temporal.TaskQueue == "" {
		errs = append(errs, fmt.Errorf("temporal task queue is required: set -temporal-task-queue or %s", EnvTemporalTaskQueue))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Load builds the configuration from args, the shell environment, and
// the working directory's .env file. Layering: flags win, then shell
// environment variables, then .env values, then defaults. It returns an
// error listing every problem found, so misconfiguration is reported in
// one go.
func Load(args []string) (*Config, error) {
	fs := flag.NewFlagSet("listener", flag.ContinueOnError)
	lf := registerListenerFlags(fs)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	dotenv, err := loadDotEnv()
	if err != nil {
		return nil, err
	}
	return lf.resolve(envLookup(dotenv))
}

// fileExists reports whether path names an existing file or directory.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
