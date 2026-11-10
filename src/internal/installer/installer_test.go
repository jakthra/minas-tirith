package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// scriptedReader serves scripted lines, one per Read, and reports
// end-of-input when they run out.
type scriptedReader struct {
	lines []string
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if len(r.lines) == 0 {
		return 0, io.EOF
	}
	line := r.lines[0] + "\n"
	r.lines = r.lines[1:]
	return copy(p, line), nil
}

// run drives Run over scripted input and returns the answers, the
// output, and the error.
func run(t *testing.T, opts Options) (*Answers, string, error) {
	t.Helper()
	var out bytes.Buffer
	opts.Out = &out
	answers, err := Run(context.Background(), opts)
	return answers, out.String(), err
}

func TestRunFullPromptSequence(t *testing.T) {
	// Provider: accept the default. Repository: an invalid answer, then
	// a valid one. Token: an ordinary line (piped input).
	in := strings.NewReader("\n\nnot-a-repo\nacme/widgets\ns3cret-token\n")

	answers, out, err := run(t, Options{In: in})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := Answers{Provider: "forgejo", Repository: "acme/widgets", Token: "s3cret-token"}
	if *answers != want {
		t.Errorf("answers = %+v, want %+v", *answers, want)
	}

	for _, want := range []string{
		"Git provider:",
		"1) forgejo",
		"Choose provider (number or name) [forgejo]: ",
		"Repository (owner/name): ",
		"is not in owner/name form", // the invalid answer is explained
		"Access token (input hidden): ",
		"Selected forgejo repository acme/widgets",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunProviderSelection(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "by number", input: "1\nacme/widgets\ntok\n", want: "forgejo"},
		{name: "by name", input: "forgejo\nacme/widgets\ntok\n", want: "forgejo"},
		{name: "unknown answer re-prompts", input: "gitlab\n\nacme/widgets\ntok\n", want: "forgejo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answers, out, err := run(t, Options{In: strings.NewReader(tt.input)})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if answers.Provider != tt.want {
				t.Errorf("provider = %q, want %q", answers.Provider, tt.want)
			}
			if tt.name == "unknown answer re-prompts" && !strings.Contains(out, `Unknown provider "gitlab"`) {
				t.Errorf("output does not explain the unknown provider:\n%s", out)
			}
		})
	}
}

func TestRunRepositoryValidation(t *testing.T) {
	tests := []struct {
		answer string
		valid  bool
	}{
		{"acme/widgets", true},
		{"Acme.Widgets_2/repo-name.v1", true},
		{"widgets", false},
		{"/widgets", false},
		{"acme/", false},
		{"acme/widgets/extra", false},
		{"acme widget/widgets", false},
		{"../widgets", false},
	}
	for _, tt := range tests {
		t.Run(tt.answer, func(t *testing.T) {
			in := strings.NewReader("\n" + tt.answer + "\nacme/widgets\ntok\n")
			answers, _, err := run(t, Options{In: in})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			want := tt.answer
			if !tt.valid {
				want = "acme/widgets" // re-prompted until valid
			}
			if answers.Repository != want {
				t.Errorf("repository = %q, want %q", answers.Repository, want)
			}
		})
	}
}

func TestRunTokenOnTerminal(t *testing.T) {
	// The terminal read consumes the token from the same stream with
	// echo disabled; the reader below stands in for term.ReadPassword.
	// The prompt flow reads stdin line by line, so the scripted stream
	// is a plain reader shared by both.
	var reads int
	in := &scriptedReader{lines: []string{"", "", "acme/widgets"}}
	opts := Options{
		In:         in,
		IsTerminal: func(io.Reader) bool { return true },
		ReadPassword: func(got io.Reader) ([]byte, error) {
			reads++
			if got != in {
				t.Errorf("ReadPassword got %T, want the input stream", got)
			}
			return []byte("typed-secret"), nil
		},
	}
	answers, out, err := run(t, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reads != 1 {
		t.Errorf("ReadPassword calls = %d, want 1", reads)
	}
	if answers.Token != "typed-secret" {
		t.Errorf("token = %q, want %q", answers.Token, "typed-secret")
	}
	if strings.Contains(out, "typed-secret") {
		t.Errorf("output echoes the token:\n%s", out)
	}
}

func TestRunPresetAnswersSkipPrompts(t *testing.T) {
	// Only the provider prompt remains; repository and token come from
	// the options.
	answers, out, err := run(t, Options{
		In:         strings.NewReader("\n"),
		Repository: "acme/widgets",
		Token:      "preset-token",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if answers.Repository != "acme/widgets" || answers.Token != "preset-token" {
		t.Errorf("answers = %+v", *answers)
	}
	for _, unwanted := range []string{"Repository (owner/name)", "Access token"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output prompts %q for a preset answer:\n%s", unwanted, out)
		}
	}
}

func TestRunInvalidPresetFails(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{name: "repository", opts: Options{Repository: "widgets"}, want: "owner/name"},
		{name: "token with whitespace", opts: Options{Repository: "acme/widgets", Token: "a b"}, want: "whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.In = strings.NewReader("\n")
			_, _, err := run(t, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

func TestRunAbortsOnEndOfInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty input", input: ""},
		{name: "before repository", input: "\n"},
		{name: "before token", input: "\nacme/widgets\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			answers, _, err := run(t, Options{In: strings.NewReader(tt.input)})
			if !errors.Is(err, ErrAborted) {
				t.Fatalf("err = %v, want ErrAborted", err)
			}
			if answers != nil {
				t.Errorf("answers = %+v, want nil", *answers)
			}
		})
	}
}

func TestRunAbortsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Options{In: strings.NewReader("\n"), Out: io.Discard})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestRunRequiresStreams(t *testing.T) {
	if _, err := Run(context.Background(), Options{Out: io.Discard}); err == nil {
		t.Error("Run without In succeeded, want an error")
	}
	if _, err := Run(context.Background(), Options{In: strings.NewReader("")}); err == nil {
		t.Error("Run without Out succeeded, want an error")
	}
}

func TestRunRejectsUnknownDefaultProvider(t *testing.T) {
	_, _, err := run(t, Options{In: strings.NewReader(""), DefaultProvider: "gitlab"})
	if err == nil || !strings.Contains(err.Error(), "not offered") {
		t.Fatalf("err = %v, want one mentioning the unoffered default", err)
	}
}
