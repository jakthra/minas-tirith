// Command install onboards a repository to Minas Tirith interactively:
// it asks which git provider to connect, which repository to watch, and
// for that repository's own access token, then creates the repository's
// webhook on the provider (with a freshly generated secret) and records
// the repository in the watched-repos config file, so the listener
// watches it.
//
// The prompt flow and the onboarding steps live in internal/installer,
// which reads and writes injectable streams and takes an injectable
// provider client, so the whole flow is tested without a terminal or a
// live Forgejo.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"minas-tirith/internal/config"
	"minas-tirith/internal/installer"
	"minas-tirith/internal/provider"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd(os.Stdin, os.Stdout, os.Stderr).ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "install:", err)
		os.Exit(1)
	}
}

// newRootCmd builds the install command tree. The streams are injected
// so tests drive the command without a terminal; newProvider is the
// provider client factory, replaced in tests by provider.Fake.
func newRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	return newRootCmdWithProvider(stdin, stdout, stderr, nil)
}

// newRootCmdWithProvider builds the command tree over the given
// provider client factory; nil uses the real Forgejo client.
func newRootCmdWithProvider(stdin io.Reader, stdout, stderr io.Writer,
	newProvider func(providerName, baseURL, token string) (provider.Client, error)) *cobra.Command {

	var (
		providerName string
		repo         string
		token        string
		installFlags config.InstallFlags
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Onboard a git repository to Minas Tirith",
		Long: `Onboard a git repository to Minas Tirith.

The installer asks for the git provider (Forgejo), the repository to
watch as owner/name, and that repository's own access token. The token
is read from the terminal without echoing it; when input is piped it is
read as an ordinary line, so scripts can drive the flow.

It then creates the repository's webhook on the provider — targeting
the listener's webhook route and signed with a freshly generated secret
— and writes the repository, its secret, and its token to the
watched-repositories config file. Restart the listener afterwards to
start watching the repository.

Every answer can be preset with a flag or the matching environment
variable (INSTALL_PROVIDER, INSTALL_REPO, INSTALL_TOKEN); a preset
answer is not asked for. Where the provider and listener live, and
which config file to write, follow the usual layering: flags, then
environment variables, then .env, then defaults.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadInstall(installFlags)
			if err != nil {
				return err
			}
			res, err := installer.Onboard(cmd.Context(), installer.OnboardOptions{
				Options: installer.Options{
					In:              stdin,
					Out:             stdout,
					DefaultProvider: providerName,
					Repository:      repo,
					Token:           token,
					IsTerminal:      func(io.Reader) bool { return isTerminal(stdin) },
				},
				ForgejoURL:    cfg.ForgejoURL,
				ListenerURL:   cfg.ListenerURL,
				ConfigPath:    cfg.WatchedConfig,
				WebhookEvents: cfg.WebhookEvents,
				NewProvider:   newProvider,
			})
			if err != nil {
				if errors.Is(err, installer.ErrAborted) || errors.Is(err, context.Canceled) {
					return errors.New("aborted")
				}
				return err
			}
			installer.WriteSummary(stdout, res)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags := cmd.Flags()
	flags.StringVar(&providerName, "provider", envOr(config.EnvInstallProvider, installer.DefaultProvider),
		"git provider to connect (env "+config.EnvInstallProvider+")")
	flags.StringVar(&repo, "repo", envOr(config.EnvInstallRepo, ""),
		"repository to watch as owner/name; skips the prompt (env "+config.EnvInstallRepo+")")
	flags.StringVar(&token, "token", envOr(config.EnvInstallToken, ""),
		"repository access token; skips the prompt (env "+config.EnvInstallToken+"; prefer the prompt, a flag is visible in the process list)")
	flags.StringVar(&installFlags.ForgejoURL, "forgejo-url", "",
		"Forgejo/Gitea instance base URL the webhook is created on (env "+config.EnvForgejoURL+", default \""+config.DefaultForgejoURL+"\")")
	flags.StringVar(&installFlags.ListenerURL, "listener-url", "",
		"listener base URL webhook deliveries target (env "+config.EnvListenerURL+", default \""+config.DefaultListenerURL+"\")")
	flags.StringVar(&installFlags.WatchedConfig, "watched-config", "",
		"watched-repositories config file the repository is written to (env "+config.EnvWatchedConfig+", default \""+config.DefaultWatchedConfig+"\")")
	flags.StringVar(&installFlags.WebhookEvents, "webhook-events", "",
		"comma-separated webhook events to subscribe (env "+config.EnvWebhookEvents+"; default: push, issue comments, merge request comments and reviews)")
	return cmd
}

// isTerminal reports whether stdin is a terminal, so the token prompt
// can disable echo. A non-terminal (piped input) reads the token as an
// ordinary line.
func isTerminal(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// envOr returns the environment variable's value when set, else def.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
