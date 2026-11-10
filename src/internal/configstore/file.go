package configstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// File is a Store over a declarative config file: a YAML document — the
// shape a Kubernetes ConfigMap mounts, and JSON parses too, YAML being
// its superset — declaring every watched repository with its own webhook
// secret and, optionally, its own provider access token:
//
//	repos:
//	  - project: BeanieGenie
//	    secret: s3cret
//	    token: beanie-forgejo-token
//	  - project: Garden
//	    secret: another-secret
//
// The file is loaded on demand, so a broken declaration fails the
// listener's startup loudly instead of its deliveries quietly.
// AddOrUpdate writes entries back, so the installer onboarding a
// repository persists it here instead of hand-editing the file.
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
	doc, err := f.parse(data)
	if err != nil {
		return nil, err
	}
	return doc.repositories(f.path)
}

// parse parses the config file's bytes into its document form.
func (f *File) parse(data []byte) (fileDocument, error) {
	var doc fileDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fileDocument{}, fmt.Errorf("parse watched config %s: %w", f.path, err)
	}
	return doc, nil
}

// AddOrUpdate persists one repository declaration to the config file:
// the entry addressed by the repository's project is updated in place,
// any other is appended. Every other entry is preserved. The file is
// rewritten in full — comments in it are not preserved — through a
// temporary file in the same directory, so a failure never leaves a
// half-written config behind. A missing file is created: the installer
// onboarding the first repository has no config to update yet.
//
// The repository's project and webhook secret are required; its token is
// optional, like in the file.
func (f *File) AddOrUpdate(repo Repository) error {
	switch {
	case repo.Project == "":
		return fmt.Errorf("update watched config %s: project is required", f.path)
	case repo.Secret == "":
		return fmt.Errorf("update watched config %s (%s): webhook secret is required", f.path, repo.Project)
	}

	doc, err := f.loadForUpdate()
	if err != nil {
		return err
	}

	entry := fileEntry{Project: repo.Project, Secret: repo.Secret, Token: repo.Token}
	found := false
	for i, e := range doc.Repos {
		if e.Project == repo.Project {
			doc.Repos[i] = entry
			found = true
			break
		}
	}
	if !found {
		doc.Repos = append(doc.Repos, entry)
	}

	// The merged document must load: validating before writing means the
	// store never persists a config its own reader rejects — including a
	// pre-existing broken entry, which is reported rather than carried
	// along silently.
	if _, err := doc.repositories(f.path); err != nil {
		return err
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("serialize watched config %s: %w", f.path, err)
	}
	return f.write(data)
}

// loadForUpdate reads the config file for writing. A missing file is an
// empty document — the write creates it — but a present, unparsable one
// is an error, so a broken config is reported, not overwritten.
func (f *File) loadForUpdate() (fileDocument, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return fileDocument{}, nil
	}
	if err != nil {
		return fileDocument{}, fmt.Errorf("read watched config: %w", err)
	}
	return f.parse(data)
}

// write replaces the config file's contents with data. The new contents
// land in a temporary file in the same directory first and are moved
// into place only once written, so a crash mid-write cannot destroy the
// existing config. A new file is created owner-readable only — the
// config holds secrets; an existing file keeps its mode.
func (f *File) write(data []byte) error {
	mode := os.FileMode(0o600)
	if info, err := os.Stat(f.path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), "."+filepath.Base(f.path)+".*")
	if err != nil {
		return fmt.Errorf("write watched config %s: %w", f.path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the move into place succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write watched config %s: %w", f.path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write watched config %s: %w", f.path, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("write watched config %s: %w", f.path, err)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		return fmt.Errorf("write watched config %s: %w", f.path, err)
	}
	return nil
}

// fileDocument is the config file's top level.
type fileDocument struct {
	Repos []fileEntry `yaml:"repos"`
}

// fileEntry is one repository declaration in the file.
type fileEntry struct {
	Project string `yaml:"project"`
	Secret  string `yaml:"secret"`
	// Token is optional: entries without a provider access token (the
	// listener needs none) serialize without the field.
	Token string `yaml:"token,omitempty"`
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
		repos = append(repos, Repository{Project: e.Project, Secret: e.Secret, Token: e.Token})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return repos, nil
}
