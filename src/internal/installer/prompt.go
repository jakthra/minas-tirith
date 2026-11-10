// The prompt primitives: reading a line, asking a question with a
// default, and reading the access token without echoing it on a
// terminal.

package installer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// prompter reads answers from one input stream and writes prompts to
// one output stream.
type prompter struct {
	ctx  context.Context
	in   *bufio.Reader
	out  io.Writer
	term bool
	// stdin is the original input stream, handed to readPassword on a
	// terminal: term.ReadPassword needs the terminal file, not the
	// buffered reader. The prompt flow reads whole lines, so the
	// buffered reader never holds input ahead of the token prompt.
	stdin        io.Reader
	readPassword func(in io.Reader) ([]byte, error)
}

// readLine reads one line from the input, without its line ending.
// End-of-input before any character reports ErrAborted.
func (p *prompter) readLine() (string, error) {
	line, err := p.in.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			if line == "" {
				return "", ErrAborted
			}
			// A final line without a newline is still an answer.
			return strings.TrimRight(line, "\r"), nil
		}
		return "", fmt.Errorf("installer: read input: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// ask writes prompt and reads a line. When the answer is empty and a
// default is given, the default is returned.
func (p *prompter) ask(prompt, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", prompt, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", prompt)
	}
	line, err := p.readLine()
	if err != nil {
		if errors.Is(err, ErrAborted) {
			fmt.Fprintln(p.out)
		}
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def, nil
	}
	return line, nil
}

// chooseProvider asks which git provider to connect, offering the
// providers by number and accepting the default on an empty answer.
func (p *prompter) chooseProvider(providers []string, def string) (string, error) {
	fmt.Fprintln(p.out, "Git provider:")
	for i, name := range providers {
		marker := " "
		if name == def {
			marker = "*"
		}
		fmt.Fprintf(p.out, "  %d) %s %s\n", i+1, name, marker)
	}
	for {
		if err := p.ctx.Err(); err != nil {
			return "", err
		}
		answer, err := p.ask("Choose provider (number or name)", def)
		if err != nil {
			return "", err
		}
		if contains(providers, answer) {
			return answer, nil
		}
		for i, name := range providers {
			if answer == fmt.Sprint(i+1) {
				return name, nil
			}
		}
		fmt.Fprintf(p.out, "Unknown provider %q; offered: %s\n", answer, strings.Join(providers, ", "))
	}
}

// askRepository asks for the repository in owner/name form, re-prompting
// until ValidateRepository accepts the answer.
func (p *prompter) askRepository() (string, error) {
	for {
		if err := p.ctx.Err(); err != nil {
			return "", err
		}
		answer, err := p.ask("Repository (owner/name)", "")
		if err != nil {
			return "", err
		}
		if err := ValidateRepository(answer); err != nil {
			fmt.Fprintln(p.out, err)
			continue
		}
		return answer, nil
	}
}

// askToken reads the repository's access token. On a terminal the echo
// is disabled for the read and the answer is not echoed; otherwise the
// token is read as an ordinary line so piped input works. The token is
// never written to the output stream.
func (p *prompter) askToken() (string, error) {
	if err := p.ctx.Err(); err != nil {
		return "", err
	}
	fmt.Fprint(p.out, "Access token (input hidden): ")
	if !p.term {
		line, err := p.readLine()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	readPassword := p.readPassword
	if readPassword == nil {
		readPassword = func(in io.Reader) ([]byte, error) {
			f, ok := in.(*os.File)
			if !ok {
				return nil, fmt.Errorf("installer: read access token: %T is not a terminal", in)
			}
			return term.ReadPassword(int(f.Fd()))
		}
	}
	secret, err := readPassword(p.stdin)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", ErrAborted
		}
		return "", fmt.Errorf("installer: read access token: %w", err)
	}
	// The user's Enter key did not echo either: end the prompt line.
	fmt.Fprintln(p.out)
	return strings.TrimSpace(string(secret)), nil
}
