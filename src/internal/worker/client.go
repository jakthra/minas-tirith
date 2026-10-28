package worker

import (
	"context"
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
)

// ClientConfig configures the connection to a Temporal server.
type ClientConfig struct {
	// HostPort is the Temporal frontend gRPC address, e.g.
	// "localhost:7233" (the local dev server's default).
	HostPort string
	// Namespace is the Temporal namespace to connect to, e.g. "default".
	Namespace string
}

// DialClient connects to the Temporal server described by cfg. The dial is
// eager: it fails if the server is unreachable, so a misconfigured worker
// exits at startup instead of silently starting nothing. SDK-internal logs
// are routed through the default slog logger.
func DialClient(ctx context.Context, cfg ClientConfig) (client.Client, error) {
	if cfg.HostPort == "" {
		return nil, fmt.Errorf("temporal host:port is required")
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("temporal namespace is required")
	}
	c, err := client.DialContext(ctx, client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
		Logger:    log.NewStructuredLogger(slog.Default()),
	})
	if err != nil {
		return nil, fmt.Errorf("connect to temporal server %s (namespace %s): %w",
			cfg.HostPort, cfg.Namespace, err)
	}
	slog.Info("connected to temporal server", "host_port", cfg.HostPort, "namespace", cfg.Namespace)
	return c, nil
}
