package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"minas-tirith/internal/config"
	"minas-tirith/internal/configstore"
	"minas-tirith/internal/provider"
)

// execute runs the command tree over the given stdin and returns its
// output and error. stdout and stderr are kept apart so tests can
// assert what the user sees where. The provider client factory is
// replaced by one returning fake.
func execute(t *testing.T, fake *provider.Fake, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	newProvider := func(providerName, _, _ string) (provider.Client, error) {
		if providerName != "forgejo" {
			t.Errorf("provider = %q", providerName)
		}
		return fake, nil
	}
	cmd := newRootCmdWithProvider(strings.NewReader(stdin), &out, &errOut, newProvider)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// isolate clears every variable the installer reads and returns a temp
// watched-config path.
func isolate(t *testing.T) string {
	t.Helper()
	for _, name := range []string{
		config.EnvInstallProvider, config.EnvInstallRepo, config.EnvInstallToken,
		config.EnvForgejoURL, config.EnvListenerURL, config.EnvWatchedConfig, config.EnvWebhookEvents,
	} {
		t.Setenv(name, "")
	}
	return filepath.Join(t.TempDir(), "watched.yaml")
}

func TestInstallOnboardsEndToEnd(t *testing.T) {
	configPath := isolate(t)
	fake := provider.NewFake()
	fake.SeedFiles("acme/widgets", "main", nil)

	stdout, _, err := execute(t, fake, "\nacme/widgets\ns3cret-token\n", "--watched-config", configPath)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	calls := fake.WebhookCalls()
	if len(calls) != 1 {
		t.Fatalf("webhook calls = %d, want 1", len(calls))
	}
	wantURL := "http://localhost:9797/v1/widgets/webhooks/forgejo"
	if calls[0].Repo != "acme/widgets" || calls[0].TargetURL != wantURL {
		t.Errorf("webhook call = %+v, want repo acme/widgets at %s", calls[0], wantURL)
	}
	if len(calls[0].Secret) != 64 {
		t.Errorf("secret length = %d, want a generated 64-char secret", len(calls[0].Secret))
	}
	if strings.Join(calls[0].Events, ",") != strings.Join(config.DefaultWebhookEvents, ",") {
		t.Errorf("events = %v, want the defaults", calls[0].Events)
	}

	repos, err := configstore.NewFile(configPath).Repositories()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if len(repos) != 1 || repos[0].Project != "widgets" || repos[0].Token != "s3cret-token" || repos[0].Secret != calls[0].Secret {
		t.Errorf("config entries = %+v", repos)
	}

	for _, want := range []string{"acme/widgets", wantURL, configPath, "Restart the listener"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
	for _, secret := range []string{"s3cret-token", calls[0].Secret} {
		if strings.Contains(stdout, secret) {
			t.Errorf("stdout leaks %q:\n%s", secret, stdout)
		}
	}
}

func TestInstallFlagsAndEnvPresetAnswers(t *testing.T) {
	configPath := isolate(t)
	fake := provider.NewFake()
	fake.SeedFiles("acme/widgets", "main", nil)
	t.Setenv(config.EnvInstallRepo, "acme/widgets")
	t.Setenv(config.EnvInstallToken, "env-token")
	t.Setenv(config.EnvListenerURL, "https://listener.example.com")

	// Only the provider prompt remains.
	stdout, _, err := execute(t, fake, "\n", "--watched-config", configPath)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if strings.Contains(stdout, "Access token") || strings.Contains(stdout, "Repository (owner/name)") {
		t.Errorf("stdout prompts for preset answers:\n%s", stdout)
	}
	wantURL := "https://listener.example.com/v1/widgets/webhooks/forgejo"
	if calls := fake.WebhookCalls(); len(calls) != 1 || calls[0].TargetURL != wantURL {
		t.Errorf("webhook calls = %+v, want the listener url from the environment", calls)
	}
	repos, err := configstore.NewFile(configPath).Repositories()
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Token != "env-token" {
		t.Errorf("config entries = %+v", repos)
	}
}

func TestInstallWebhookFailureLeavesNoConfig(t *testing.T) {
	configPath := isolate(t)
	fake := provider.NewFake() // repository not seeded: CreateWebhook fails

	stdout, _, err := execute(t, fake, "\nacme/widgets\ntok\n", "--watched-config", configPath)
	if err == nil || !strings.Contains(err.Error(), "no config entry written") {
		t.Fatalf("err = %v, want the webhook failure", err)
	}
	if strings.Contains(stdout, "Onboarded") {
		t.Errorf("stdout reports success after a failed webhook:\n%s", stdout)
	}
	if _, statErr := configstore.NewFile(configPath).Repositories(); statErr == nil {
		t.Error("config file written after a failed webhook creation")
	}
}

func TestInstallAborted(t *testing.T) {
	isolate(t)
	_, _, err := execute(t, provider.NewFake(), "")
	if err == nil || err.Error() != "aborted" {
		t.Fatalf("err = %v, want %q", err, "aborted")
	}
}

func TestInstallRejectsArguments(t *testing.T) {
	isolate(t)
	if _, _, err := execute(t, provider.NewFake(), "", "extra"); err == nil {
		t.Fatal("Execute with a positional argument succeeded, want an error")
	}
}

func TestInstallRejectsInvalidRepositoryFlag(t *testing.T) {
	isolate(t)
	_, _, err := execute(t, provider.NewFake(), "\n", "--repo", "widgets")
	if err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Fatalf("err = %v, want one mentioning owner/name", err)
	}
}

func TestInstallRejectsInvalidListenerURL(t *testing.T) {
	isolate(t)
	_, _, err := execute(t, provider.NewFake(), "\nacme/widgets\ntok\n", "--listener-url", "localhost:9797")
	if err == nil || !strings.Contains(err.Error(), "invalid listener url") {
		t.Fatalf("err = %v, want an invalid listener url error", err)
	}
}
