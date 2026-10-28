package config

import (
	"errors"
	"flag"
	"fmt"
)

// The Temporal environment variables, defaults, and TemporalConfig type
// are declared in config.go and shared by the listener and worker.

// Provider environment variables recognized by the worker: the
// Forgejo/Gitea instance the agent activities talk to.
const (
	EnvForgejoURL   = "FORGEJO_URL"   // Forgejo instance base URL
	EnvForgejoToken = "FORGEJO_TOKEN" // Forgejo access token
)

// LLM environment variables recognized by the worker: the LLM the
// agent activities reason through, over an OpenAI-compatible API.
const (
	EnvLLMBaseURL = "LLM_BASE_URL" // OpenAI-compatible API base URL
	EnvLLMAPIKey  = "LLM_API_KEY"  // LLM provider API key
	EnvLLMModel   = "LLM_MODEL"    // model requested for reasoning
)

// DefaultForgejoURL matches the local development instance
// (forgejo/docker-compose.yaml).
const DefaultForgejoURL = "http://localhost:3000"

// LLM defaults: the Mistral API, which is OpenAI-compatible, so any
// OpenAI-compatible endpoint (OpenAI, Ollama, vLLM, LM Studio,
// OpenRouter, ...) is a LLM_BASE_URL override away. The model alias
// tracks the current Mistral Small release.
const (
	DefaultLLMBaseURL = "https://api.mistral.ai/v1"
	DefaultLLMModel   = "mistral-small-latest"
)

// ProviderConfig configures the Forgejo provider client the agent
// activities use for repository access (fetch files, create branches,
// commit, open merge requests).
type ProviderConfig struct {
	// URL is the Forgejo (or Gitea) instance base URL, e.g.
	// "http://localhost:3000".
	URL string
	// Token is the provider access token authenticating API calls.
	Token string
}

// LLMConfig configures the LLM client the agent activities use for
// reasoning (generate implementation tickets, interpret review
// comments). The API is OpenAI-compatible, so the base URL selects the
// provider; the API key is required once the refinement activities
// consume the client (TASK-7).
type LLMConfig struct {
	// BaseURL is the OpenAI-compatible API base URL, e.g.
	// "https://api.mistral.ai/v1".
	BaseURL string
	// APIKey authenticates LLM API calls (Authorization: Bearer).
	APIKey string
	// Model is the model name requested for reasoning.
	Model string
}

// WorkerConfig is the agent worker configuration: the Temporal settings,
// the Forgejo provider settings, and the LLM settings. The worker is a
// pure Temporal worker: it polls task queues and executes workflows and
// activities; the listener starts the workflows.
type WorkerConfig struct {
	// Temporal configures the Temporal connection and workers.
	Temporal TemporalConfig
	// Provider configures the Forgejo client used by agent activities.
	Provider ProviderConfig
	// LLM configures the LLM client used by agent activities.
	LLM LLMConfig
}

// LoadWorker builds the worker configuration from args, the shell
// environment, and the working directory's .env file, the same
// layering as the listener: flags win, then shell environment
// variables, then .env values, then defaults.
func LoadWorker(args []string) (*WorkerConfig, error) {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	hostPort := fs.String("temporal-host-port", "",
		"Temporal frontend gRPC address (env TEMPORAL_HOST_PORT, default \"localhost:7233\")")
	namespace := fs.String("temporal-namespace", "",
		"Temporal namespace (env TEMPORAL_NAMESPACE, default \"default\")")
	taskQueue := fs.String("temporal-task-queue", "",
		"base task queue for agent workers (env TEMPORAL_TASK_QUEUE, default \"minas-tirith\")")
	forgejoURL := fs.String("forgejo-url", "",
		"Forgejo/Gitea instance base URL for agent repository access (env FORGEJO_URL, default \"http://localhost:3000\")")
	forgejoToken := fs.String("forgejo-token", "",
		"Forgejo access token for agent repository access (env FORGEJO_TOKEN)")
	llmBaseURL := fs.String("llm-base-url", "",
		"OpenAI-compatible LLM API base URL for agent reasoning (env LLM_BASE_URL, default \"https://api.mistral.ai/v1\")")
	llmAPIKey := fs.String("llm-api-key", "",
		"LLM provider API key for agent reasoning (env LLM_API_KEY)")
	llmModel := fs.String("llm-model", "",
		"LLM model for agent reasoning (env LLM_MODEL, default \"mistral-small-latest\")")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	dotenv, err := loadDotEnv()
	if err != nil {
		return nil, err
	}
	getenv := envLookup(dotenv)

	cfg := &WorkerConfig{
		Temporal: TemporalConfig{
			HostPort:  firstNonEmpty(*hostPort, getenv(EnvTemporalHostPort), DefaultTemporalHostPort),
			Namespace: firstNonEmpty(*namespace, getenv(EnvTemporalNamespace), DefaultTemporalNamespace),
			TaskQueue: firstNonEmpty(*taskQueue, getenv(EnvTemporalTaskQueue), DefaultTemporalTaskQueue),
		},
		Provider: ProviderConfig{
			URL:   firstNonEmpty(*forgejoURL, getenv(EnvForgejoURL), DefaultForgejoURL),
			Token: firstNonEmpty(*forgejoToken, getenv(EnvForgejoToken)),
		},
		LLM: LLMConfig{
			BaseURL: firstNonEmpty(*llmBaseURL, getenv(EnvLLMBaseURL), DefaultLLMBaseURL),
			APIKey:  firstNonEmpty(*llmAPIKey, getenv(EnvLLMAPIKey)),
			Model:   firstNonEmpty(*llmModel, getenv(EnvLLMModel), DefaultLLMModel),
		},
	}

	var errs []error
	if cfg.Temporal.HostPort == "" {
		errs = append(errs, fmt.Errorf("temporal host:port is required: set -temporal-host-port or %s", EnvTemporalHostPort))
	}
	if cfg.Temporal.Namespace == "" {
		errs = append(errs, fmt.Errorf("temporal namespace is required: set -temporal-namespace or %s", EnvTemporalNamespace))
	}
	if cfg.Temporal.TaskQueue == "" {
		errs = append(errs, fmt.Errorf("temporal task queue is required: set -temporal-task-queue or %s", EnvTemporalTaskQueue))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}
