// The installer's configuration (cmd/install): where the provider
// instance and the listener live, which watched-repos config file to
// write, and which webhook events to subscribe to. The layering is the
// one every binary shares — flags win, then shell environment
// variables, then the working directory's .env file, then defaults.

package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Installer environment variables.
const (
	EnvListenerURL     = "INSTALL_LISTENER_URL"   // listener base URL webhooks target
	EnvWebhookEvents   = "INSTALL_WEBHOOK_EVENTS" // comma-separated webhook events to subscribe
	EnvInstallProvider = "INSTALL_PROVIDER"       // git provider to connect (cmd/install)
	EnvInstallRepo     = "INSTALL_REPO"           // repository to watch, owner/name
	EnvInstallToken    = "INSTALL_TOKEN"          // repository access token
)

// DefaultListenerURL is where the listener serves webhooks in local
// development: DefaultListenAddr (":9797") on the host.
const DefaultListenerURL = "http://localhost:9797"

// DefaultWebhookEvents are the Forgejo webhook events the agents need:
// pushes (backlog commits), merge request conversation comments, and
// merge request reviews (inline diff comments). It mirrors the event
// list in the README's manual wiring instructions.
var DefaultWebhookEvents = []string{
	"push",
	"issue_comment",
	"pull_request_comment",
	"pull_request_review_comment",
	"pull_request_review_approved",
	"pull_request_review_rejected",
}

// InstallConfig is the installer configuration.
type InstallConfig struct {
	// ForgejoURL is the provider instance base URL the installer calls
	// to create the webhook, e.g. "http://localhost:3000".
	ForgejoURL string
	// ListenerURL is the listener's base URL webhook deliveries target;
	// the installer appends the webhook route to it.
	ListenerURL string
	// WatchedConfig is the watched-repositories config file the
	// repository entry is written to.
	WatchedConfig string
	// WebhookEvents are the provider webhook events to subscribe to.
	WebhookEvents []string
}

// InstallFlags are the installer's flag values, already parsed by the
// command. An empty field falls through to the environment and then the
// default.
type InstallFlags struct {
	// ForgejoURL is the provider instance base URL.
	ForgejoURL string
	// ListenerURL is the listener base URL webhook deliveries target.
	ListenerURL string
	// WatchedConfig is the watched-repositories config file to write.
	WatchedConfig string
	// WebhookEvents is the comma-separated webhook event list.
	WebhookEvents string
}

// LoadInstall builds the installer configuration from the parsed flags,
// the shell environment, and the working directory's .env file.
// Layering: flags win, then shell environment variables, then .env
// values, then defaults. It returns an error listing every problem
// found.
func LoadInstall(flags InstallFlags) (*InstallConfig, error) {
	dotenv, err := loadDotEnv()
	if err != nil {
		return nil, err
	}
	getenv := envLookup(dotenv)

	cfg := &InstallConfig{
		ForgejoURL:    firstNonEmpty(flags.ForgejoURL, getenv(EnvForgejoURL), DefaultForgejoURL),
		ListenerURL:   firstNonEmpty(flags.ListenerURL, getenv(EnvListenerURL), DefaultListenerURL),
		WatchedConfig: firstNonEmpty(flags.WatchedConfig, getenv(EnvWatchedConfig), DefaultWatchedConfig),
	}

	var errs []error
	if err := validateBaseURL("forgejo url", cfg.ForgejoURL); err != nil {
		errs = append(errs, fmt.Errorf("%w (set -forgejo-url or %s)", err, EnvForgejoURL))
	}
	if err := validateBaseURL("listener url", cfg.ListenerURL); err != nil {
		errs = append(errs, fmt.Errorf("%w (set -listener-url or %s)", err, EnvListenerURL))
	}
	eventsValue := firstNonEmpty(flags.WebhookEvents, getenv(EnvWebhookEvents))
	if eventsValue == "" {
		cfg.WebhookEvents = append([]string(nil), DefaultWebhookEvents...)
	} else {
		cfg.WebhookEvents = splitList(eventsValue)
		if len(cfg.WebhookEvents) == 0 {
			errs = append(errs, fmt.Errorf("webhook events list is empty: set -webhook-events or %s", EnvWebhookEvents))
		}
	}
	if cfg.WatchedConfig == "" {
		errs = append(errs, fmt.Errorf("watched config path is required: set -watched-config or %s", EnvWatchedConfig))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validateBaseURL checks that raw is an absolute http(s) URL with a host.
func validateBaseURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid %s %q: expected an absolute http(s) URL", name, raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid %s %q: scheme must be http or https", name, raw)
	}
	return nil
}

// splitList splits a comma-separated list, trimming blanks and
// dropping empty entries.
func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
