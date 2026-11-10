package main

import (
	"bytes"
	"strings"
	"testing"
)

// execute runs the command tree over the given stdin and returns its
// output and error. stdout and stderr are kept apart so tests can
// assert what the user sees where.
func execute(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := newRootCmd(strings.NewReader(stdin), &out, &errOut)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestInstallPromptsAndPrintsAnswers(t *testing.T) {
	t.Setenv("INSTALL_PROVIDER", "")
	t.Setenv("INSTALL_REPO", "")
	t.Setenv("INSTALL_TOKEN", "")

	stdout, _, err := execute(t, "\nacme/widgets\ns3cret-token\n")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, want := range []string{
		"Git provider:",
		"1) forgejo",
		"Repository (owner/name): ",
		"Access token (input hidden): ",
		"provider:   forgejo",
		"repository: acme/widgets",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "s3cret-token") {
		t.Errorf("stdout echoes the token:\n%s", stdout)
	}
}

func TestInstallFlagsSkipPrompts(t *testing.T) {
	t.Setenv("INSTALL_PROVIDER", "")
	t.Setenv("INSTALL_REPO", "")
	t.Setenv("INSTALL_TOKEN", "")

	// Only the provider prompt remains: flags preset the rest.
	stdout, _, err := execute(t, "\n", "--repo", "acme/widgets", "--token", "tok")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, unwanted := range []string{"Repository (owner/name)", "Access token"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("stdout prompts %q despite the flag:\n%s", unwanted, stdout)
		}
	}
	if !strings.Contains(stdout, "repository: acme/widgets") {
		t.Errorf("stdout missing the repository:\n%s", stdout)
	}
}

func TestInstallEnvironmentPresets(t *testing.T) {
	t.Setenv("INSTALL_PROVIDER", "forgejo")
	t.Setenv("INSTALL_REPO", "acme/widgets")
	t.Setenv("INSTALL_TOKEN", "env-token")

	stdout, _, err := execute(t, "\n")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(stdout, "Access token") {
		t.Errorf("stdout prompts for the token despite INSTALL_TOKEN:\n%s", stdout)
	}
	if !strings.Contains(stdout, "repository: acme/widgets") {
		t.Errorf("stdout missing the repository:\n%s", stdout)
	}
}

func TestInstallAborted(t *testing.T) {
	t.Setenv("INSTALL_PROVIDER", "")
	t.Setenv("INSTALL_REPO", "")
	t.Setenv("INSTALL_TOKEN", "")

	_, _, err := execute(t, "")
	if err == nil || err.Error() != "aborted" {
		t.Fatalf("err = %v, want %q", err, "aborted")
	}
}

func TestInstallRejectsArguments(t *testing.T) {
	if _, _, err := execute(t, "", "extra"); err == nil {
		t.Fatal("Execute with a positional argument succeeded, want an error")
	}
}

func TestInstallRejectsInvalidRepositoryFlag(t *testing.T) {
	t.Setenv("INSTALL_PROVIDER", "")
	t.Setenv("INSTALL_REPO", "")
	t.Setenv("INSTALL_TOKEN", "")

	_, _, err := execute(t, "\n", "--repo", "widgets")
	if err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Fatalf("err = %v, want one mentioning owner/name", err)
	}
}
