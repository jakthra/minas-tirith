// The onboarding flow: the prompt sequence, then the two side effects
// that connect a repository — creating the webhook on the provider and
// recording the repository in the watched-repos config file.

package installer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"minas-tirith/internal/configstore"
	"minas-tirith/internal/provider"
)

// SecretBytes is the length of a generated webhook secret: 32 random
// bytes, hex-encoded to 64 characters. The secret signs webhook
// deliveries (HMAC-SHA256), so it must be unguessable.
const SecretBytes = 32

// OnboardOptions configures Onboard: the prompt flow options plus the
// onboarding targets.
type OnboardOptions struct {
	// Options configures the prompt flow.
	Options
	// ForgejoURL is the provider instance base URL the webhook is
	// created on.
	ForgejoURL string
	// ListenerURL is the listener base URL; the webhook targets the
	// route ListenerURL + "/v1/{project}/webhooks/{provider}".
	ListenerURL string
	// ConfigPath is the watched-repositories config file the entry is
	// written to.
	ConfigPath string
	// WebhookEvents are the provider webhook events to subscribe to.
	WebhookEvents []string
	// NewProvider builds the provider client for the selected provider
	// and access token. Nil uses the real Forgejo client; tests inject
	// provider.Fake.
	NewProvider func(providerName, baseURL, token string) (provider.Client, error)
	// GenerateSecret returns a fresh webhook secret. Nil generates
	// SecretBytes random bytes, hex-encoded; tests replace it to make
	// the secret predictable.
	GenerateSecret func() (string, error)
}

// Result is what Onboard configured.
type Result struct {
	// Provider is the provider the repository was connected to.
	Provider string
	// Repository is the repository, as owner/name.
	Repository string
	// Project is the project name the listener watches it as: the
	// repository name, the {project} segment of the webhook route.
	Project string
	// WebhookURL is the address deliveries are sent to.
	WebhookURL string
	// WebhookID identifies the created webhook on the provider.
	WebhookID int64
	// ConfigPath is the config file the entry was written to.
	ConfigPath string
}

// Onboard runs the interactive flow and connects the repository: it
// creates the webhook on the provider with a freshly generated secret,
// then writes the repository entry — project, secret, and per-repo
// token — to the watched-repos config file so the listener watches it.
//
// The webhook is created first: when that fails, no config entry is
// written, so the config never names a repository whose deliveries
// cannot arrive. A failed config write is reported together with the
// webhook that now exists, so the user knows what to remove.
func Onboard(ctx context.Context, opts OnboardOptions) (*Result, error) {
	answers, err := Run(ctx, opts.Options)
	if err != nil {
		return nil, err
	}
	if opts.ForgejoURL == "" {
		return nil, errors.New("installer: forgejo url is required")
	}
	if opts.ListenerURL == "" {
		return nil, errors.New("installer: listener url is required")
	}
	if opts.ConfigPath == "" {
		return nil, errors.New("installer: watched config path is required")
	}
	if len(opts.WebhookEvents) == 0 {
		return nil, errors.New("installer: webhook events are required")
	}

	project, err := Project(answers.Repository)
	if err != nil {
		return nil, err
	}
	webhookURL, err := WebhookURL(opts.ListenerURL, project, answers.Provider)
	if err != nil {
		return nil, err
	}

	newProvider := opts.NewProvider
	if newProvider == nil {
		newProvider = defaultProvider
	}
	client, err := newProvider(answers.Provider, opts.ForgejoURL, answers.Token)
	if err != nil {
		return nil, fmt.Errorf("installer: build %s client: %w", answers.Provider, err)
	}

	generateSecret := opts.GenerateSecret
	if generateSecret == nil {
		generateSecret = GenerateSecret
	}
	secret, err := generateSecret()
	if err != nil {
		return nil, fmt.Errorf("installer: generate webhook secret: %w", err)
	}

	hook, err := client.CreateWebhook(ctx, answers.Repository, webhookURL, secret, opts.WebhookEvents)
	if err != nil {
		return nil, fmt.Errorf("installer: create webhook on %s: %w (no config entry written)", answers.Repository, err)
	}

	store := configstore.NewFile(opts.ConfigPath)
	if err := store.AddOrUpdate(configstore.Repository{
		Project: project,
		Secret:  secret,
		Token:   answers.Token,
	}); err != nil {
		return nil, fmt.Errorf("installer: webhook %d created on %s at %s, but writing the config failed: %w",
			hook.ID, answers.Repository, webhookURL, err)
	}

	return &Result{
		Provider:   answers.Provider,
		Repository: answers.Repository,
		Project:    project,
		WebhookURL: webhookURL,
		WebhookID:  hook.ID,
		ConfigPath: opts.ConfigPath,
	}, nil
}

// Project returns the project name a repository is watched as: its
// name, the last segment of owner/name. The repository root/BeanieGenie
// is watched as "BeanieGenie", the {project} segment of the webhook
// route.
func Project(repository string) (string, error) {
	if err := ValidateRepository(repository); err != nil {
		return "", err
	}
	_, name, _ := strings.Cut(repository, "/")
	return name, nil
}

// WebhookURL builds the address the listener serves a repository's
// webhook deliveries on: listenerURL + "/v1/{project}/webhooks/{provider}",
// the listener's webhook route.
func WebhookURL(listenerURL, project, providerName string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(listenerURL), "/")
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("installer: invalid listener url %q: expected an absolute http(s) URL", listenerURL)
	}
	if project == "" || providerName == "" {
		return "", errors.New("installer: project and provider are required for the webhook url")
	}
	return base + "/v1/" + url.PathEscape(project) + "/webhooks/" + url.PathEscape(providerName), nil
}

// GenerateSecret returns a fresh webhook secret: SecretBytes random
// bytes from the system's cryptographic source, hex-encoded.
func GenerateSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// defaultProvider builds the real provider client for the selected
// provider. Forgejo is the only provider offered, and Gitea shares its
// API.
func defaultProvider(providerName, baseURL, token string) (provider.Client, error) {
	switch providerName {
	case ProviderForgejo:
		return provider.NewForgejoClient(baseURL, token)
	default:
		return nil, fmt.Errorf("unsupported git provider %q", providerName)
	}
}

// WriteSummary prints what Onboard configured. It names the repository,
// the webhook URL, and the config file; it never prints the access
// token or the webhook secret.
func WriteSummary(w io.Writer, res *Result) {
	fmt.Fprintf(w, "\nOnboarded %s repository %s\n", res.Provider, res.Repository)
	fmt.Fprintf(w, "  project:     %s\n", res.Project)
	fmt.Fprintf(w, "  webhook:     %s (id %d)\n", res.WebhookURL, res.WebhookID)
	fmt.Fprintf(w, "  config file: %s\n", res.ConfigPath)
	fmt.Fprintln(w, "\nRestart the listener to start watching the repository.")
}
