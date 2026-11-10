// Command install onboards a repository to Minas Tirith interactively:
// it asks which git provider to connect, which repository to watch, and
// for that repository's own access token, then prints what it collected.
//
// The prompt flow lives in internal/installer, which reads and writes
// injectable streams so it is tested without a terminal. Creating the
// webhook and recording the repository in the watched-repos config
// store are the next step (TASK-27.4); this command stops after the
// answers.
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

	"minas-tirith/internal/installer"
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
// so tests drive the command without a terminal.
func newRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	var (
		provider string
		repo     string
		token    string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Onboard a git repository to Minas Tirith",
		Long: `Onboard a git repository to Minas Tirith.

The installer asks for the git provider (Forgejo), the repository to
watch as owner/name, and that repository's own access token. The token
is read from the terminal without echoing it; when input is piped it is
read as an ordinary line, so scripts can drive the flow.

Every answer can be preset with a flag or the matching environment
variable (INSTALL_PROVIDER, INSTALL_REPO, INSTALL_TOKEN); a preset
answer is not asked for.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			answers, err := installer.Run(cmd.Context(), installer.Options{
				In:              stdin,
				Out:             stdout,
				DefaultProvider: provider,
				Repository:      repo,
				Token:           token,
				IsTerminal:      func(in io.Reader) bool { return isTerminal(stdin) },
			})
			if err != nil {
				if errors.Is(err, installer.ErrAborted) || errors.Is(err, context.Canceled) {
					return errors.New("aborted")
				}
				return err
			}
			fmt.Fprintf(stdout, "\nprovider:   %s\nrepository: %s\n", answers.Provider, answers.Repository)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	flags := cmd.Flags()
	flags.StringVar(&provider, "provider", envOr("INSTALL_PROVIDER", installer.DefaultProvider),
		"git provider to connect (env INSTALL_PROVIDER)")
	flags.StringVar(&repo, "repo", envOr("INSTALL_REPO", ""),
		"repository to watch as owner/name; skips the prompt (env INSTALL_REPO)")
	flags.StringVar(&token, "token", envOr("INSTALL_TOKEN", ""),
		"repository access token; skips the prompt (env INSTALL_TOKEN; prefer the prompt, a flag is visible in the process list)")
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
