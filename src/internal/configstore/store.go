// Package configstore is the declarative configuration store for watched
// repositories: the repositories the listener watches, each with the
// webhook secret that authenticates that repository's deliveries.
//
// Store is the seam between the listener and wherever the declarations
// live. File reads a declarative YAML config file — the shape a
// Kubernetes ConfigMap mounts; Static serves a fixed list for wiring and
// tests. A future Kubernetes operator or CRD controller implements the
// same interface against its own source, and the listener consumes it
// unchanged.
package configstore

// Repository is one watched repository declaration.
type Repository struct {
	// Project is the project name the listener addresses the repository
	// by: the {project} segment of the webhook route. The repository
	// root/BeanieGenie is watched as "BeanieGenie".
	Project string

	// Secret is the webhook secret authenticating this repository's
	// deliveries. Each repository carries its own secret; there is no
	// shared fallback.
	Secret string
}

// Store loads watched repositories and their webhook secrets from a
// declarative source.
type Store interface {
	// Repositories returns every watched repository, each with its own
	// webhook secret.
	Repositories() ([]Repository, error)
}

// Static is a Store over a fixed list of declarations: the wiring point
// for callers that already hold the repositories in memory (tests, and
// store implementations backed by other sources).
type Static []Repository

// Repositories implements Store, returning a copy so later mutation of
// the results cannot change the store.
func (s Static) Repositories() ([]Repository, error) {
	out := make([]Repository, len(s))
	copy(out, s)
	return out, nil
}
