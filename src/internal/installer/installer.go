// Package installer drives the interactive onboarding flow of the
// install binary (cmd/install): it asks which git provider to connect,
// which repository to watch, and for that repository's own access
// token, and returns the answers.
//
// The flow is a pure prompt sequence over injectable input and output
// streams, so tests drive it without a terminal. It makes no provider
// API calls and writes no configuration: creating the webhook and
// recording the repository in the watched-repos config store are the
// caller's job once Run returns.
//
// The access token is never echoed: on a terminal it is read with the
// echo disabled, and it is never written back to the output stream —
// not in the prompts, not in the confirmation. Piped input (scripts,
// tests) reads the token as an ordinary line.
package installer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ProviderForgejo is the git provider the installer offers today.
const ProviderForgejo = "forgejo"

// Providers lists the git providers the installer offers, in the order
// they are presented. Forgejo is the only one: other providers are out
// of scope until the provider client grows a second implementation.
var Providers = []string{ProviderForgejo}

// DefaultProvider is the provider selected when the user accepts the
// default.
const DefaultProvider = ProviderForgejo

// ErrAborted reports that the input ended before the flow completed:
// the user answered end-of-input (Ctrl-D) or the stream closed. The
// partial answers are discarded.
var ErrAborted = errors.New("installer: input ended before the flow completed")

// Answers is what the flow collected.
type Answers struct {
	// Provider is the selected git provider: one of Providers.
	Provider string
	// Repository is the repository to watch, as "owner/name".
	Repository string
	// Token is the repository's own provider access token.
	Token string
}

// Options configures Run.
type Options struct {
	// In is the prompt input (usually os.Stdin).
	In io.Reader
	// Out receives the prompts and the confirmation (usually
	// os.Stdout).
	Out io.Writer
	// Providers lists the providers to offer; nil means Providers.
	Providers []string
	// DefaultProvider is preselected when the user accepts the
	// default; empty means the first offered provider.
	DefaultProvider string
	// Repository preselects the repository (owner/name) and skips its
	// prompt; empty asks for it.
	Repository string
	// Token preselects the access token and skips its prompt; empty
	// asks for it.
	Token string
	// IsTerminal reports whether In is a terminal. When it does, the
	// token is read with the echo disabled; otherwise it is read as an
	// ordinary line. Nil treats In as not a terminal.
	IsTerminal func(in io.Reader) bool
	// ReadPassword reads a secret line from a terminal In without
	// echoing it. Nil uses the standard terminal reader; tests replace
	// it to exercise the terminal path without a TTY.
	ReadPassword func(in io.Reader) ([]byte, error)
}

// Run drives the interactive flow and returns the answers. It fails
// with ErrAborted when the input ends early, and with a validation
// error only when a preset (Options.Repository or Options.Token) is
// invalid: interactive answers are re-prompted until they are valid.
func Run(ctx context.Context, opts Options) (*Answers, error) {
	if opts.In == nil {
		return nil, errors.New("installer: Options.In is required")
	}
	if opts.Out == nil {
		return nil, errors.New("installer: Options.Out is required")
	}
	providers := opts.Providers
	if len(providers) == 0 {
		providers = Providers
	}
	defaultProvider := opts.DefaultProvider
	if defaultProvider == "" {
		defaultProvider = providers[0]
	}
	if !contains(providers, defaultProvider) {
		return nil, fmt.Errorf("installer: default provider %q is not offered (offered: %s)",
			defaultProvider, strings.Join(providers, ", "))
	}

	term := opts.IsTerminal != nil && opts.IsTerminal(opts.In)
	p := &prompter{
		ctx:          ctx,
		in:           bufio.NewReader(opts.In),
		out:          opts.Out,
		term:         term,
		stdin:        opts.In,
		readPassword: opts.ReadPassword,
	}

	answers := &Answers{}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	provider, err := p.chooseProvider(providers, defaultProvider)
	if err != nil {
		return nil, err
	}
	answers.Provider = provider

	repo := strings.TrimSpace(opts.Repository)
	if repo != "" {
		if err := ValidateRepository(repo); err != nil {
			return nil, err
		}
	} else {
		repo, err = p.askRepository()
		if err != nil {
			return nil, err
		}
	}
	answers.Repository = repo

	token := strings.TrimSpace(opts.Token)
	if token == "" {
		token, err = p.askToken()
		if err != nil {
			return nil, err
		}
	}
	if err := ValidateToken(token); err != nil {
		return nil, err
	}
	answers.Token = token

	// The confirmation names the provider and repository only: the
	// token never reaches the output stream.
	fmt.Fprintf(p.out, "\nSelected %s repository %s\n", answers.Provider, answers.Repository)
	return answers, nil
}

// ValidateRepository checks that repo is a provider repository name in
// the "owner/name" form: exactly two non-empty path segments, each
// made of letters, digits, '.', '_' or '-', and neither "." nor "..".
func ValidateRepository(repo string) error {
	owner, name, ok := strings.Cut(repo, "/")
	switch {
	case !ok:
		return fmt.Errorf("installer: repository %q is not in owner/name form", repo)
	case owner == "" || name == "":
		return fmt.Errorf("installer: repository %q is not in owner/name form: both parts are required", repo)
	case strings.Contains(name, "/"):
		return fmt.Errorf("installer: repository %q has more than one '/'", repo)
	}
	for _, part := range []string{owner, name} {
		if !validSegment(part) {
			return fmt.Errorf("installer: repository %q has an invalid part %q: use letters, digits, '.', '_' or '-'", repo, part)
		}
	}
	return nil
}

// ValidateToken checks that an access token is present and carries no
// whitespace, which would be stripped or break the provider request.
func ValidateToken(token string) error {
	if token == "" {
		return errors.New("installer: the access token is required")
	}
	if strings.ContainsFunc(token, func(r rune) bool { return r == ' ' || r == '\t' || r == '\r' || r == '\n' }) {
		return errors.New("installer: the access token must not contain whitespace")
	}
	return nil
}

// validSegment reports whether s is a usable repository name segment.
func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
