package config

import (
	"os"
	"testing"
	"time"
)

// writeDotEnv chdirs the test into a fresh temp directory and, when
// content is non-empty, writes it as the .env file there. .env loads
// from the working directory, so tests that exercise it must run in a
// directory they control.
func writeDotEnv(t *testing.T, content string) {
	t.Helper()
	t.Chdir(t.TempDir())
	if content == "" {
		return
	}
	if err := os.WriteFile(DotEnvFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDotEnv(t *testing.T) {
	t.Run("missing file is not an error", func(t *testing.T) {
		writeDotEnv(t, "") // no .env in this directory

		values, err := loadDotEnv()
		if err != nil {
			t.Fatalf("loadDotEnv: %v", err)
		}
		if len(values) != 0 {
			t.Errorf("values = %#v, want none", values)
		}
	})

	t.Run("reads values from the working directory", func(t *testing.T) {
		writeDotEnv(t, "FORGEJO_TOKEN=token\n")

		values, err := loadDotEnv()
		if err != nil {
			t.Fatalf("loadDotEnv: %v", err)
		}
		if got := values["FORGEJO_TOKEN"]; got != "token" {
			t.Errorf("FORGEJO_TOKEN = %q, want %q", got, "token")
		}
	})
}

func TestParseDotEnv(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"key and value", "A=b", map[string]string{"A": "b"}},
		{"spaces around key and value are trimmed", "  A  =  b  ", map[string]string{"A": "b"}},
		{"blank lines and comments are skipped", "# header\n\n   \nA=b\n# trailing\n", map[string]string{"A": "b"}},
		{"export prefix", "export A=b", map[string]string{"A": "b"}},
		{"double-quoted value keeps spaces", `A="b c"`, map[string]string{"A": "b c"}},
		{"single-quoted value keeps spaces", "A='b c'", map[string]string{"A": "b c"}},
		{"empty quoted value", `A=""`, map[string]string{"A": ""}},
		{"inline comment ends an unquoted value", "A=b # comment", map[string]string{"A": "b"}},
		{"comment right after the equals sign empties the value", "A= # comment", map[string]string{"A": ""}},
		{"hash without whitespace stays in the value", "A=b#c", map[string]string{"A": "b#c"}},
		{"value may contain equals", "A=b=c", map[string]string{"A": "b=c"}},
		{"empty value", "A=", map[string]string{"A": ""}},
		{"crlf line endings", "A=b\r\nB=c\r\n", map[string]string{"A": "b", "B": "c"}},
		{"later lines overwrite earlier ones", "A=b\nA=c", map[string]string{"A": "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDotEnv([]byte(tt.in))
			if err != nil {
				t.Fatalf("parseDotEnv: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("values = %#v, want %#v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("values[%s] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestParseDotEnvErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"missing equals", "just-a-key", `.env:1: expected KEY=VALUE, got "just-a-key"`},
		{"empty key", "=value", `.env:1: expected KEY=VALUE, got "=value"`},
		{"key with spaces", "a key=value", `.env:1: key "a key" contains whitespace`},
		{"unterminated double quote", `A="b`, `.env:1: A: unterminated double-quoted value`},
		{"unterminated single quote", "A='b", `.env:1: A: unterminated single-quoted value`},
		{"line number is reported", "A=b\nB c\n", `.env:2: expected KEY=VALUE, got "B c"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDotEnv([]byte(tt.in))
			if err == nil {
				t.Fatalf("parseDotEnv(%q) succeeded, want error", tt.in)
			}
			if err.Error() != tt.want {
				t.Errorf("error = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadFromDotEnv(t *testing.T) {
	watched := writeWatchedConfig(t, "repos:\n  - project: BeanieGenie\n    secret: file-secret\n")
	dotenv := "LISTEN_ADDR=127.0.0.1:9001\n" +
		"WATCHED_CONFIG=" + watched + "\n" +
		"DEDUPE_TTL=2h\n" +
		"TEMPORAL_HOST_PORT=file-temporal:7233\n" +
		"TEMPORAL_NAMESPACE=file-ns\n" +
		"TEMPORAL_TASK_QUEUE=file-queue\n"

	t.Run("values load from the file", func(t *testing.T) {
		writeDotEnv(t, dotenv)

		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.ListenAddr != "127.0.0.1:9001" {
			t.Errorf("listen addr = %q, want the file's 127.0.0.1:9001", cfg.ListenAddr)
		}
		if cfg.DedupeTTL != 2*time.Hour {
			t.Errorf("dedupe ttl = %v, want the file's 2h", cfg.DedupeTTL)
		}
		wantTemporal := TemporalConfig{HostPort: "file-temporal:7233", Namespace: "file-ns", TaskQueue: "file-queue"}
		if cfg.Temporal != wantTemporal {
			t.Errorf("temporal = %+v, want %+v", cfg.Temporal, wantTemporal)
		}
		repos, err := cfg.Store.Repositories()
		if err != nil {
			t.Fatalf("load watched repos: %v", err)
		}
		if len(repos) != 1 || repos[0].Project != "BeanieGenie" || repos[0].Secret != "file-secret" {
			t.Errorf("watched repos = %#v, want the file's BeanieGenie with file-secret", repos)
		}
	})

	t.Run("shell wins over the file", func(t *testing.T) {
		writeDotEnv(t, dotenv)
		t.Setenv(EnvListenAddr, "127.0.0.1:9002")
		t.Setenv(EnvTemporalHostPort, "shell-temporal:7233")

		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.ListenAddr != "127.0.0.1:9002" {
			t.Errorf("listen addr = %q, want the shell's 127.0.0.1:9002", cfg.ListenAddr)
		}
		if cfg.Temporal.HostPort != "shell-temporal:7233" {
			t.Errorf("temporal host = %q, want the shell's shell-temporal:7233", cfg.Temporal.HostPort)
		}
		// The file still fills the gaps the shell leaves open.
		if cfg.DedupeTTL != 2*time.Hour {
			t.Errorf("dedupe ttl = %v, want the file's 2h", cfg.DedupeTTL)
		}
	})

	t.Run("flags win over shell and file", func(t *testing.T) {
		writeDotEnv(t, dotenv)
		t.Setenv(EnvListenAddr, "127.0.0.1:9002")
		t.Setenv(EnvTemporalNamespace, "shell-ns")

		cfg, err := Load([]string{
			"-listen", ":7070",
			"-dedupe-ttl", "30m",
			"-temporal-host-port", "flag-temporal:7233",
			"-temporal-namespace", "flag-ns",
		})
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.ListenAddr != ":7070" {
			t.Errorf("listen addr = %q, want the flag's :7070", cfg.ListenAddr)
		}
		if cfg.DedupeTTL != 30*time.Minute {
			t.Errorf("dedupe ttl = %v, want the flag's 30m", cfg.DedupeTTL)
		}
		wantTemporal := TemporalConfig{HostPort: "flag-temporal:7233", Namespace: "flag-ns", TaskQueue: "file-queue"}
		if cfg.Temporal != wantTemporal {
			t.Errorf("temporal = %+v, want %+v (flags over shell and file, file filling the rest)", cfg.Temporal, wantTemporal)
		}
	})

	t.Run("missing file runs on shell and defaults", func(t *testing.T) {
		writeDotEnv(t, "")
		t.Setenv(EnvListenAddr, "127.0.0.1:9002")
		t.Setenv(EnvWatchedConfig, watched)

		cfg, err := Load(nil)
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if cfg.ListenAddr != "127.0.0.1:9002" {
			t.Errorf("listen addr = %q, want the shell's 127.0.0.1:9002", cfg.ListenAddr)
		}
		if cfg.DedupeTTL != 24*time.Hour {
			t.Errorf("dedupe ttl = %v, want the default 24h", cfg.DedupeTTL)
		}
		wantTemporal := TemporalConfig{
			HostPort:  DefaultTemporalHostPort,
			Namespace: DefaultTemporalNamespace,
			TaskQueue: DefaultTemporalTaskQueue,
		}
		if cfg.Temporal != wantTemporal {
			t.Errorf("temporal = %+v, want the defaults %+v", cfg.Temporal, wantTemporal)
		}
	})
}

func TestLoadWorkerFromDotEnv(t *testing.T) {
	dotenv := "TEMPORAL_HOST_PORT=file-temporal:7233\n" +
		"TEMPORAL_NAMESPACE=file-ns\n" +
		"TEMPORAL_TASK_QUEUE=file-queue\n" +
		"FORGEJO_URL=http://file:3000\n" +
		"FORGEJO_TOKEN=file-token\n" +
		"LLM_BASE_URL=http://file:11434/v1\n" +
		"LLM_API_KEY=file-key\n" +
		"LLM_MODEL=file-model\n"

	t.Run("values load from the file", func(t *testing.T) {
		writeDotEnv(t, dotenv)

		cfg, err := LoadWorker(nil)
		if err != nil {
			t.Fatalf("load worker: %v", err)
		}
		wantTemporal := TemporalConfig{HostPort: "file-temporal:7233", Namespace: "file-ns", TaskQueue: "file-queue"}
		if cfg.Temporal != wantTemporal {
			t.Errorf("temporal = %+v, want %+v", cfg.Temporal, wantTemporal)
		}
		wantProvider := ProviderConfig{URL: "http://file:3000", Token: "file-token"}
		if cfg.Provider != wantProvider {
			t.Errorf("provider = %+v, want %+v", cfg.Provider, wantProvider)
		}
		wantLLM := LLMConfig{BaseURL: "http://file:11434/v1", APIKey: "file-key", Model: "file-model"}
		if cfg.LLM != wantLLM {
			t.Errorf("llm = %+v, want %+v", cfg.LLM, wantLLM)
		}
	})

	t.Run("shell wins over the file", func(t *testing.T) {
		writeDotEnv(t, dotenv)
		t.Setenv(EnvForgejoToken, "shell-token")
		t.Setenv(EnvLLMAPIKey, "shell-key")

		cfg, err := LoadWorker(nil)
		if err != nil {
			t.Fatalf("load worker: %v", err)
		}
		if cfg.Provider.Token != "shell-token" {
			t.Errorf("forgejo token = %q, want the shell's shell-token", cfg.Provider.Token)
		}
		if cfg.LLM.APIKey != "shell-key" {
			t.Errorf("llm api key = %q, want the shell's shell-key", cfg.LLM.APIKey)
		}
		// The file still fills the gaps the shell leaves open.
		if cfg.Provider.URL != "http://file:3000" {
			t.Errorf("forgejo url = %q, want the file's http://file:3000", cfg.Provider.URL)
		}
		if cfg.LLM.Model != "file-model" {
			t.Errorf("llm model = %q, want the file's file-model", cfg.LLM.Model)
		}
	})

	t.Run("flags win over shell and file", func(t *testing.T) {
		writeDotEnv(t, dotenv)
		t.Setenv(EnvForgejoToken, "shell-token")

		cfg, err := LoadWorker([]string{"-forgejo-token", "flag-token", "-llm-model", "flag-model"})
		if err != nil {
			t.Fatalf("load worker: %v", err)
		}
		if cfg.Provider.Token != "flag-token" {
			t.Errorf("forgejo token = %q, want the flag's flag-token", cfg.Provider.Token)
		}
		if cfg.LLM.Model != "flag-model" {
			t.Errorf("llm model = %q, want the flag's flag-model", cfg.LLM.Model)
		}
		// The file still fills the gaps flags and shell leave open.
		if cfg.Provider.URL != "http://file:3000" {
			t.Errorf("forgejo url = %q, want the file's http://file:3000", cfg.Provider.URL)
		}
	})

	t.Run("missing file runs on shell and defaults", func(t *testing.T) {
		writeDotEnv(t, "")
		t.Setenv(EnvForgejoToken, "shell-token")

		cfg, err := LoadWorker(nil)
		if err != nil {
			t.Fatalf("load worker: %v", err)
		}
		wantTemporal := TemporalConfig{
			HostPort:  DefaultTemporalHostPort,
			Namespace: DefaultTemporalNamespace,
			TaskQueue: DefaultTemporalTaskQueue,
		}
		if cfg.Temporal != wantTemporal {
			t.Errorf("temporal = %+v, want the defaults %+v", cfg.Temporal, wantTemporal)
		}
		wantProvider := ProviderConfig{URL: DefaultForgejoURL, Token: "shell-token"}
		if cfg.Provider != wantProvider {
			t.Errorf("provider = %+v, want %+v", cfg.Provider, wantProvider)
		}
		wantLLM := LLMConfig{BaseURL: DefaultLLMBaseURL, APIKey: "", Model: DefaultLLMModel}
		if cfg.LLM != wantLLM {
			t.Errorf("llm = %+v, want the defaults %+v", cfg.LLM, wantLLM)
		}
	})
}

func TestLoadDotEnvMalformed(t *testing.T) {
	t.Run("listener fails startup naming the file and line", func(t *testing.T) {
		writeDotEnv(t, "LISTEN_ADDR=:9797\nnot-an-entry\n")

		_, err := Load(nil)
		if err == nil {
			t.Fatal("expected error for malformed .env")
		}
		want := `.env:2: expected KEY=VALUE, got "not-an-entry"`
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err, want)
		}
	})

	t.Run("worker fails startup naming the file and line", func(t *testing.T) {
		writeDotEnv(t, "FORGEJO_TOKEN\n")

		_, err := LoadWorker(nil)
		if err == nil {
			t.Fatal("expected error for malformed .env")
		}
		want := `.env:1: expected KEY=VALUE, got "FORGEJO_TOKEN"`
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err, want)
		}
	})
}
