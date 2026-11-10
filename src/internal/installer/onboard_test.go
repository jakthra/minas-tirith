package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"minas-tirith/internal/configstore"
	"minas-tirith/internal/provider"
)

// fixedSecret makes the generated secret predictable in tests.
func fixedSecret() (string, error) { return "generated-secret", nil }

// onboardOptions returns options that drive the prompt flow from a
// scripted stdin and connect acme/widgets through fake.
func onboardOptions(t *testing.T, fake *provider.Fake, configPath, stdin string) OnboardOptions {
	t.Helper()
	return OnboardOptions{
		Options:       Options{In: strings.NewReader(stdin), Out: io.Discard},
		ForgejoURL:    "http://forgejo.test:3000",
		ListenerURL:   "http://listener.test:9797/",
		ConfigPath:    configPath,
		WebhookEvents: []string{"push", "issue_comment"},
		NewProvider: func(providerName, baseURL, token string) (provider.Client, error) {
			if providerName != ProviderForgejo {
				t.Errorf("provider = %q, want %q", providerName, ProviderForgejo)
			}
			if baseURL != "http://forgejo.test:3000" {
				t.Errorf("base url = %q", baseURL)
			}
			if token != "repo-token" {
				t.Errorf("token = %q, want the prompted token", token)
			}
			return fake, nil
		},
		GenerateSecret: fixedSecret,
	}
}

func TestOnboardFullFlow(t *testing.T) {
	fake := provider.NewFake()
	fake.SeedFiles("acme/widgets", "main", map[string]string{"README.md": "hi"})
	configPath := filepath.Join(t.TempDir(), "watched.yaml")

	var out bytes.Buffer
	opts := onboardOptions(t, fake, configPath, "\nacme/widgets\nrepo-token\n")
	opts.Out = &out

	res, err := Onboard(context.Background(), opts)
	if err != nil {
		t.Fatalf("Onboard: %v", err)
	}

	wantURL := "http://listener.test:9797/v1/widgets/webhooks/forgejo"
	calls := fake.WebhookCalls()
	if len(calls) != 1 {
		t.Fatalf("webhook calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.Repo != "acme/widgets" || call.TargetURL != wantURL || call.Secret != "generated-secret" {
		t.Errorf("webhook call = %+v, want repo acme/widgets, url %s, generated secret", call, wantURL)
	}
	if strings.Join(call.Events, ",") != "push,issue_comment" {
		t.Errorf("events = %v", call.Events)
	}
	if hooks := fake.Webhooks("acme/widgets"); len(hooks) != 1 {
		t.Errorf("webhooks stored = %d, want 1", len(hooks))
	}

	// The config entry names the project, the generated secret, and the
	// per-repo token.
	repos, err := configstore.NewFile(configPath).Repositories()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	want := []configstore.Repository{{Project: "widgets", Secret: "generated-secret", Token: "repo-token"}}
	if len(repos) != 1 || repos[0] != want[0] {
		t.Errorf("config entries = %+v, want %+v", repos, want)
	}

	if res.Project != "widgets" || res.WebhookURL != wantURL || res.ConfigPath != configPath || res.WebhookID == 0 {
		t.Errorf("result = %+v", *res)
	}

	WriteSummary(&out, res)
	summary := out.String()
	for _, want := range []string{"acme/widgets", wantURL, configPath} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
	for _, secret := range []string{"repo-token", "generated-secret"} {
		if strings.Contains(summary, secret) {
			t.Errorf("summary leaks %q:\n%s", secret, summary)
		}
	}
}

func TestOnboardPreservesOtherEntries(t *testing.T) {
	fake := provider.NewFake()
	fake.SeedFiles("acme/widgets", "main", nil)
	configPath := filepath.Join(t.TempDir(), "watched.yaml")
	existing := configstore.Repository{Project: "Garden", Secret: "garden-secret", Token: "garden-token"}
	if err := configstore.NewFile(configPath).AddOrUpdate(existing); err != nil {
		t.Fatal(err)
	}

	opts := onboardOptions(t, fake, configPath, "\nacme/widgets\nrepo-token\n")
	if _, err := Onboard(context.Background(), opts); err != nil {
		t.Fatalf("Onboard: %v", err)
	}

	repos, err := configstore.NewFile(configPath).Repositories()
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0] != existing || repos[1].Project != "widgets" {
		t.Errorf("config entries = %+v", repos)
	}
}

func TestOnboardWebhookFailureLeavesNoConfigEntry(t *testing.T) {
	fake := provider.NewFake()
	// No repository seeded: CreateWebhook fails with ErrNotFound.
	configPath := filepath.Join(t.TempDir(), "watched.yaml")

	opts := onboardOptions(t, fake, configPath, "\nacme/widgets\nrepo-token\n")
	_, err := Onboard(context.Background(), opts)
	if err == nil || !errors.Is(err, provider.ErrNotFound) {
		t.Fatalf("err = %v, want the provider error", err)
	}
	if !strings.Contains(err.Error(), "no config entry written") {
		t.Errorf("err = %v, want it to say no config entry was written", err)
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Errorf("config file exists after a failed webhook creation (stat err = %v)", statErr)
	}
}

func TestOnboardConfigWriteFailureIsReported(t *testing.T) {
	fake := provider.NewFake()
	fake.SeedFiles("acme/widgets", "main", nil)
	// A directory is not a writable config file: the write fails.
	configPath := t.TempDir()

	opts := onboardOptions(t, fake, configPath, "\nacme/widgets\nrepo-token\n")
	_, err := Onboard(context.Background(), opts)
	if err == nil {
		t.Fatal("Onboard succeeded, want a config write error")
	}
	for _, want := range []string{"webhook 1 created on acme/widgets", "writing the config failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
	if hooks := fake.Webhooks("acme/widgets"); len(hooks) != 1 {
		t.Errorf("webhooks = %d, want the created webhook to remain", len(hooks))
	}
}

func TestOnboardProviderFactoryFailure(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "watched.yaml")
	opts := onboardOptions(t, provider.NewFake(), configPath, "\nacme/widgets\nrepo-token\n")
	opts.NewProvider = func(string, string, string) (provider.Client, error) {
		return nil, errors.New("bad token")
	}
	if _, err := Onboard(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("err = %v, want the factory error", err)
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Error("config file exists after a failed provider client build")
	}
}

func TestOnboardSecretGenerationFailure(t *testing.T) {
	fake := provider.NewFake()
	fake.SeedFiles("acme/widgets", "main", nil)
	opts := onboardOptions(t, fake, filepath.Join(t.TempDir(), "watched.yaml"), "\nacme/widgets\nrepo-token\n")
	opts.GenerateSecret = func() (string, error) { return "", errors.New("no entropy") }
	if _, err := Onboard(context.Background(), opts); err == nil || !strings.Contains(err.Error(), "no entropy") {
		t.Fatalf("err = %v, want the generator error", err)
	}
	if len(fake.WebhookCalls()) != 0 {
		t.Error("webhook created without a secret")
	}
}

func TestOnboardAbortsBeforeSideEffects(t *testing.T) {
	fake := provider.NewFake()
	configPath := filepath.Join(t.TempDir(), "watched.yaml")
	opts := onboardOptions(t, fake, configPath, "")
	if _, err := Onboard(context.Background(), opts); !errors.Is(err, ErrAborted) {
		t.Fatalf("err = %v, want ErrAborted", err)
	}
	if len(fake.WebhookCalls()) != 0 {
		t.Error("webhook created for an aborted flow")
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Error("config written for an aborted flow")
	}
}

func TestOnboardRejectsMissingTargets(t *testing.T) {
	tests := []struct {
		name string
		edit func(*OnboardOptions)
		want string
	}{
		{name: "forgejo url", edit: func(o *OnboardOptions) { o.ForgejoURL = "" }, want: "forgejo url"},
		{name: "listener url", edit: func(o *OnboardOptions) { o.ListenerURL = "" }, want: "listener url"},
		{name: "config path", edit: func(o *OnboardOptions) { o.ConfigPath = "" }, want: "config path"},
		{name: "events", edit: func(o *OnboardOptions) { o.WebhookEvents = nil }, want: "webhook events"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := onboardOptions(t, provider.NewFake(), "unused.yaml", "\nacme/widgets\nrepo-token\n")
			tt.edit(&opts)
			if _, err := Onboard(context.Background(), opts); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want one mentioning %q", err, tt.want)
			}
		})
	}
}

func TestProject(t *testing.T) {
	tests := []struct {
		repo, want string
		wantErr    bool
	}{
		{repo: "root/BeanieGenie", want: "BeanieGenie"},
		{repo: "acme/widgets", want: "widgets"},
		{repo: "widgets", wantErr: true},
	}
	for _, tt := range tests {
		got, err := Project(tt.repo)
		if (err != nil) != tt.wantErr {
			t.Errorf("Project(%q) err = %v, wantErr %v", tt.repo, err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("Project(%q) = %q, want %q", tt.repo, got, tt.want)
		}
	}
}

func TestWebhookURL(t *testing.T) {
	tests := []struct {
		name, listener, project, provider, want string
		wantErr                                 bool
	}{
		{
			name:     "route",
			listener: "http://localhost:9797",
			project:  "BeanieGenie",
			provider: "forgejo",
			want:     "http://localhost:9797/v1/BeanieGenie/webhooks/forgejo",
		},
		{
			name:     "trailing slash trimmed",
			listener: "https://listener.example.com/",
			project:  "widgets",
			provider: "forgejo",
			want:     "https://listener.example.com/v1/widgets/webhooks/forgejo",
		},
		{name: "relative url", listener: "localhost:9797", project: "w", provider: "forgejo", wantErr: true},
		{name: "missing project", listener: "http://l:1", project: "", provider: "forgejo", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := WebhookURL(tt.listener, tt.project, tt.provider)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("url = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGenerateSecret(t *testing.T) {
	a, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != SecretBytes*2 {
		t.Errorf("secret length = %d, want %d hex characters", len(a), SecretBytes*2)
	}
	if a == b {
		t.Error("two generated secrets are equal")
	}
	if strings.ContainsFunc(a, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) {
		t.Errorf("secret %q is not hex", a)
	}
}
