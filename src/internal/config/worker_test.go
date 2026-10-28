package config

import (
	"testing"
)

func TestLoadWorkerDefaults(t *testing.T) {
	for _, name := range []string{EnvTemporalHostPort, EnvTemporalNamespace, EnvTemporalTaskQueue} {
		t.Setenv(name, "")
	}

	cfg, err := LoadWorker(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := TemporalConfig{
		HostPort:  DefaultTemporalHostPort,
		Namespace: DefaultTemporalNamespace,
		TaskQueue: DefaultTemporalTaskQueue,
	}
	if cfg.Temporal != want {
		t.Errorf("temporal = %+v, want %+v", cfg.Temporal, want)
	}
}

func TestLoadWorkerTemporalConfig(t *testing.T) {
	t.Run("env", func(t *testing.T) {
		t.Setenv(EnvTemporalHostPort, "temporal.example.com:7233")
		t.Setenv(EnvTemporalNamespace, "production")
		t.Setenv(EnvTemporalTaskQueue, "agents")

		cfg, err := LoadWorker(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := TemporalConfig{HostPort: "temporal.example.com:7233", Namespace: "production", TaskQueue: "agents"}
		if cfg.Temporal != want {
			t.Errorf("temporal = %+v, want %+v", cfg.Temporal, want)
		}
	})

	t.Run("flags win over env", func(t *testing.T) {
		t.Setenv(EnvTemporalHostPort, "env-temporal:7233")
		t.Setenv(EnvTemporalNamespace, "env-ns")
		t.Setenv(EnvTemporalTaskQueue, "env-queue")

		cfg, err := LoadWorker([]string{
			"-temporal-host-port", "flag-temporal:7233",
			"-temporal-namespace", "flag-ns",
			"-temporal-task-queue", "flag-queue",
		})
		if err != nil {
			t.Fatal(err)
		}
		want := TemporalConfig{HostPort: "flag-temporal:7233", Namespace: "flag-ns", TaskQueue: "flag-queue"}
		if cfg.Temporal != want {
			t.Errorf("temporal = %+v, want %+v", cfg.Temporal, want)
		}
	})
}

func TestLoadWorkerFlagsWinOverEnv(t *testing.T) {
	t.Setenv(EnvTemporalHostPort, "from-env:7233")

	cfg, err := LoadWorker([]string{"-temporal-host-port", "from-flag:7233"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Temporal.HostPort != "from-flag:7233" {
		t.Errorf("host port = %q, want from-flag:7233", cfg.Temporal.HostPort)
	}
}

func TestLoadWorkerRejectsUnknownFlag(t *testing.T) {
	if _, err := LoadWorker([]string{"-no-such-flag"}); err == nil {
		t.Error("unknown flag accepted")
	}
}

func TestLoadWorkerProviderConfig(t *testing.T) {
	t.Setenv(EnvForgejoURL, "")
	t.Setenv(EnvForgejoToken, "")

	t.Run("defaults", func(t *testing.T) {
		cfg, err := LoadWorker(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := ProviderConfig{URL: DefaultForgejoURL, Token: ""}
		if cfg.Provider != want {
			t.Errorf("provider = %+v, want %+v", cfg.Provider, want)
		}
	})

	t.Run("env", func(t *testing.T) {
		t.Setenv(EnvForgejoURL, "https://forgejo.example.com")
		t.Setenv(EnvForgejoToken, "env-token")
		cfg, err := LoadWorker(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := ProviderConfig{URL: "https://forgejo.example.com", Token: "env-token"}
		if cfg.Provider != want {
			t.Errorf("provider = %+v, want %+v", cfg.Provider, want)
		}
	})

	t.Run("flags win over env", func(t *testing.T) {
		t.Setenv(EnvForgejoURL, "https://forgejo.example.com")
		t.Setenv(EnvForgejoToken, "env-token")
		cfg, err := LoadWorker([]string{"-forgejo-url", "http://flag:3000", "-forgejo-token", "flag-token"})
		if err != nil {
			t.Fatal(err)
		}
		want := ProviderConfig{URL: "http://flag:3000", Token: "flag-token"}
		if cfg.Provider != want {
			t.Errorf("provider = %+v, want %+v", cfg.Provider, want)
		}
	})
}

func TestLoadWorkerLLMConfig(t *testing.T) {
	t.Setenv(EnvLLMBaseURL, "")
	t.Setenv(EnvLLMAPIKey, "")
	t.Setenv(EnvLLMModel, "")

	t.Run("defaults to the mistral api", func(t *testing.T) {
		cfg, err := LoadWorker(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := LLMConfig{BaseURL: DefaultLLMBaseURL, APIKey: "", Model: DefaultLLMModel}
		if cfg.LLM != want {
			t.Errorf("llm = %+v, want %+v", cfg.LLM, want)
		}
	})

	t.Run("env", func(t *testing.T) {
		t.Setenv(EnvLLMBaseURL, "http://localhost:11434/v1")
		t.Setenv(EnvLLMAPIKey, "env-key")
		t.Setenv(EnvLLMModel, "llama3.1")
		cfg, err := LoadWorker(nil)
		if err != nil {
			t.Fatal(err)
		}
		want := LLMConfig{BaseURL: "http://localhost:11434/v1", APIKey: "env-key", Model: "llama3.1"}
		if cfg.LLM != want {
			t.Errorf("llm = %+v, want %+v", cfg.LLM, want)
		}
	})

	t.Run("flags win over env", func(t *testing.T) {
		t.Setenv(EnvLLMBaseURL, "http://localhost:11434/v1")
		t.Setenv(EnvLLMAPIKey, "env-key")
		t.Setenv(EnvLLMModel, "llama3.1")
		cfg, err := LoadWorker([]string{"-llm-base-url", "https://api.mistral.ai/v1", "-llm-api-key", "flag-key", "-llm-model", "mistral-large-latest"})
		if err != nil {
			t.Fatal(err)
		}
		want := LLMConfig{BaseURL: "https://api.mistral.ai/v1", APIKey: "flag-key", Model: "mistral-large-latest"}
		if cfg.LLM != want {
			t.Errorf("llm = %+v, want %+v", cfg.LLM, want)
		}
	})
}
