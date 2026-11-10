package configstore

import (
	"os"
	"path/filepath"
	"testing"
)

// writeConfig writes content to a fresh config file and returns its path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "watched.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// multiRepoYAML declares two repositories, each with its own secret.
const multiRepoYAML = `repos:
  - project: BeanieGenie
    secret: beanie-secret
  - project: Garden
    secret: garden-secret
`

func TestFileLoadsMultipleRepos(t *testing.T) {
	store := NewFile(writeConfig(t, multiRepoYAML))

	repos, err := store.Repositories()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []Repository{
		{Project: "BeanieGenie", Secret: "beanie-secret"},
		{Project: "Garden", Secret: "garden-secret"},
	}
	if len(repos) != len(want) {
		t.Fatalf("loaded %d repos, want %d: %+v", len(repos), len(want), repos)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Errorf("repos[%d] = %+v, want %+v", i, repos[i], want[i])
		}
	}
}

func TestFileParsesJSON(t *testing.T) {
	// YAML is a JSON superset, so a JSON config file loads unchanged —
	// the same file works however the deployment ships it.
	store := NewFile(writeConfig(t,
		`{"repos":[{"project":"BeanieGenie","secret":"s3cret"}]}`))

	repos, err := store.Repositories()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []Repository{{Project: "BeanieGenie", Secret: "s3cret"}}
	if len(repos) != 1 || repos[0] != want[0] {
		t.Errorf("repos = %+v, want %+v", repos, want)
	}
}

func TestFileEmptyConfig(t *testing.T) {
	// An empty file is no watched repositories; whether that is an error
	// is the consumer's call, not the store's.
	store := NewFile(writeConfig(t, ""))

	repos, err := store.Repositories()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("repos = %+v, want none", repos)
	}
}

func TestFileMissing(t *testing.T) {
	store := NewFile(filepath.Join(t.TempDir(), "no-such-file.yaml"))

	if _, err := store.Repositories(); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestFileUnparsable(t *testing.T) {
	store := NewFile(writeConfig(t, "repos: [unclosed"))

	if _, err := store.Repositories(); err == nil {
		t.Fatal("expected error for unparsable config file")
	}
}

func TestFileValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string // error fragments that must all be reported
	}{
		{
			"entry without a project",
			"repos:\n  - secret: s\n",
			[]string{"repos[0]", "project is required"},
		},
		{
			"entry without a secret",
			"repos:\n  - project: BeanieGenie\n",
			[]string{"repos[0] (BeanieGenie)", "webhook secret is required"},
		},
		{
			"duplicate project",
			"repos:\n  - project: BeanieGenie\n    secret: a\n  - project: BeanieGenie\n    secret: b\n",
			[]string{"repos[1] (BeanieGenie)", "declared more than once"},
		},
		{
			"reports every problem in one error",
			"repos:\n  - secret: s\n  - project: Garden\n",
			[]string{"repos[0]", "project is required", "repos[1] (Garden)", "webhook secret is required"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewFile(writeConfig(t, tt.content))
			repos, err := store.Repositories()
			if err == nil {
				t.Fatalf("expected error, got repos %+v", repos)
			}
			for _, want := range tt.want {
				if !contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// tokenedRepoYAML declares two repositories, one carrying an access
// token and one without: the token is optional per entry.
const tokenedRepoYAML = `repos:
  - project: BeanieGenie
    secret: beanie-secret
    token: beanie-token
  - project: Garden
    secret: garden-secret
`

func TestFileLoadsTokens(t *testing.T) {
	// The access token rides alongside the webhook secret; entries
	// without one load with an empty token.
	store := NewFile(writeConfig(t, tokenedRepoYAML))

	repos, err := store.Repositories()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []Repository{
		{Project: "BeanieGenie", Secret: "beanie-secret", Token: "beanie-token"},
		{Project: "Garden", Secret: "garden-secret"},
	}
	if len(repos) != len(want) {
		t.Fatalf("loaded %d repos, want %d: %+v", len(repos), len(want), repos)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Errorf("repos[%d] = %+v, want %+v", i, repos[i], want[i])
		}
	}
}

func TestFileAddOrUpdateAddsProject(t *testing.T) {
	path := writeConfig(t, tokenedRepoYAML)
	store := NewFile(path)

	if err := store.AddOrUpdate(Repository{Project: "Orchard", Secret: "orchard-secret", Token: "orchard-token"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Reload from disk: the write must persist, not just mutate memory,
	// and every other entry — including its token — must survive.
	repos, err := store.Repositories()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := []Repository{
		{Project: "BeanieGenie", Secret: "beanie-secret", Token: "beanie-token"},
		{Project: "Garden", Secret: "garden-secret"},
		{Project: "Orchard", Secret: "orchard-secret", Token: "orchard-token"},
	}
	if len(repos) != len(want) {
		t.Fatalf("loaded %d repos, want %d: %+v", len(repos), len(want), repos)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Errorf("repos[%d] = %+v, want %+v", i, repos[i], want[i])
		}
	}

	// The token serializes into the entry.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(data), "token: orchard-token") {
		t.Errorf("written config %q does not serialize the token", data)
	}
}

func TestFileAddOrUpdateUpdatesProject(t *testing.T) {
	path := writeConfig(t, tokenedRepoYAML)
	store := NewFile(path)

	if err := store.AddOrUpdate(Repository{Project: "BeanieGenie", Secret: "rotated-secret", Token: "rotated-token"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Reload from disk: the existing project is replaced in place — not
	// moved to the end — and its neighbors are untouched.
	repos, err := store.Repositories()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := []Repository{
		{Project: "BeanieGenie", Secret: "rotated-secret", Token: "rotated-token"},
		{Project: "Garden", Secret: "garden-secret"},
	}
	if len(repos) != len(want) {
		t.Fatalf("loaded %d repos, want %d: %+v", len(repos), len(want), repos)
	}
	for i := range want {
		if repos[i] != want[i] {
			t.Errorf("repos[%d] = %+v, want %+v", i, repos[i], want[i])
		}
	}
}

func TestFileAddOrUpdateOmitsEmptyToken(t *testing.T) {
	// An entry without a token serializes without the field, so
	// rewrites do not litter the file with empty tokens.
	path := writeConfig(t, multiRepoYAML)
	store := NewFile(path)

	if err := store.AddOrUpdate(Repository{Project: "Orchard", Secret: "orchard-secret"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(data), "token:") {
		t.Errorf("written config %q serializes an empty token", data)
	}
}

func TestFileAddOrUpdateCreatesFile(t *testing.T) {
	// The installer onboarding the first repository has no config file
	// to update yet, so a write against a missing file creates it.
	path := filepath.Join(t.TempDir(), "watched.yaml")
	store := NewFile(path)

	if err := store.AddOrUpdate(Repository{Project: "BeanieGenie", Secret: "s3cret", Token: "t0ken"}); err != nil {
		t.Fatalf("create: %v", err)
	}

	repos, err := store.Repositories()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	want := []Repository{{Project: "BeanieGenie", Secret: "s3cret", Token: "t0ken"}}
	if len(repos) != 1 || repos[0] != want[0] {
		t.Errorf("repos = %+v, want %+v", repos, want)
	}

	// The config holds secrets, so a file the store creates is
	// owner-readable only.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("created config has mode %o, want 600", perm)
	}
}

func TestFileAddOrUpdateValidation(t *testing.T) {
	tests := []struct {
		name    string
		repo    Repository
		content string // the existing config file
		want    []string
	}{
		{
			"repository without a project",
			Repository{Secret: "s"},
			multiRepoYAML,
			[]string{"project is required"},
		},
		{
			"repository without a secret",
			Repository{Project: "BeanieGenie"},
			multiRepoYAML,
			[]string{"(BeanieGenie)", "webhook secret is required"},
		},
		{
			"existing entry without a secret",
			Repository{Project: "Orchard", Secret: "s"},
			"repos:\n  - project: BeanieGenie\n",
			[]string{"repos[0] (BeanieGenie)", "webhook secret is required"},
		},
		{
			"duplicate project in the existing config",
			Repository{Project: "Orchard", Secret: "s"},
			"repos:\n  - project: BeanieGenie\n    secret: a\n  - project: BeanieGenie\n    secret: b\n",
			[]string{"repos[1] (BeanieGenie)", "declared more than once"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfig(t, tt.content)
			store := NewFile(path)

			err := store.AddOrUpdate(tt.repo)
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range tt.want {
				if !contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}

			// A refused write leaves the file exactly as it was.
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.content {
				t.Errorf("config file was modified: %q, want %q", data, tt.content)
			}
		})
	}
}

func TestFileAddOrUpdateUnparsableFile(t *testing.T) {
	// A broken config is reported, not overwritten.
	path := writeConfig(t, "repos: [unclosed")
	store := NewFile(path)

	if err := store.AddOrUpdate(Repository{Project: "BeanieGenie", Secret: "s"}); err == nil {
		t.Fatal("expected error for unparsable config file")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "repos: [unclosed" {
		t.Errorf("config file was modified: %q", data)
	}
}

func TestStatic(t *testing.T) {
	store := Static{{Project: "BeanieGenie", Secret: "s3cret"}}

	repos, err := store.Repositories()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(repos) != 1 || repos[0] != (Repository{Project: "BeanieGenie", Secret: "s3cret"}) {
		t.Errorf("repos = %+v, want BeanieGenie with its secret", repos)
	}

	// Results are copies: mutating them cannot change the store.
	repos[0].Secret = "changed"
	again, err := store.Repositories()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again[0].Secret != "s3cret" {
		t.Errorf("store changed through results: %+v", again)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
