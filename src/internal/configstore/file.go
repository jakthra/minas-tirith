package configstore

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// File is a Store over a declarative config file: a YAML document — the
// shape a Kubernetes ConfigMap mounts, and JSON parses too, YAML being
// its superset — declaring every watched repository with its own webhook
// secret:
//
//	repos:
//	  - project: BeanieGenie
//	    secret: s3cret
//	  - project: Garden
//	    secret: another-secret
//
// The file is loaded on demand, so a broken declaration fails the
// listener's startup loudly instead of its deliveries quietly.
type File struct {
	path string
}

// NewFile returns a Store reading the declarative config at path.
func NewFile(path string) *File {
	return &File{path: path}
}

// Path returns the config file's location, for logs and error context.
func (f *File) Path() string { return f.path }

// Repositories implements Store.
func (f *File) Repositories() ([]Repository, error) {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return nil, fmt.Errorf("read watched config: %w", err)
	}
	var doc fileDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse watched config %s: %w", f.path, err)
	}
	return doc.repositories(f.path)
}

// fileDocument is the config file's top level.
type fileDocument struct {
	Repos []fileEntry `yaml:"repos"`
}

// fileEntry is one repository declaration in the file.
type fileEntry struct {
	Project string `yaml:"project"`
	Secret  string `yaml:"secret"`
}

// repositories validates the parsed declarations and maps them onto
// repositories. Every problem is reported in one error, so a broken
// config file is fixed in one pass.
func (d fileDocument) repositories(path string) ([]Repository, error) {
	var errs []error
	repos := make([]Repository, 0, len(d.Repos))
	seen := make(map[string]struct{}, len(d.Repos))
	for i, e := range d.Repos {
		switch {
		case e.Project == "":
			errs = append(errs, fmt.Errorf("%s: repos[%d]: project is required", path, i))
			continue
		case e.Secret == "":
			errs = append(errs, fmt.Errorf("%s: repos[%d] (%s): webhook secret is required", path, i, e.Project))
			continue
		}
		if _, dup := seen[e.Project]; dup {
			errs = append(errs, fmt.Errorf("%s: repos[%d] (%s): project is declared more than once", path, i, e.Project))
			continue
		}
		seen[e.Project] = struct{}{}
		repos = append(repos, Repository{Project: e.Project, Secret: e.Secret})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return repos, nil
}
