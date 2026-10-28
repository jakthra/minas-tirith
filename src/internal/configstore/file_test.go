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
