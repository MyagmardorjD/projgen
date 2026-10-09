// Package options defines what a developer can choose and validates the
// combination before anything is written to disk.
package options

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Choice is one selectable value shown to the developer.
type Choice struct {
	Value string
	Label string
}

var (
	Languages = []Choice{
		{"go", "Go"},
	}
	Frameworks = map[string][]Choice{
		"go": {
			{"gin", "Gin"},
			{"echo", "Echo"},
			{"fiber", "Fiber"},
			{"nethttp", "net/http (standard library)"},
		},
	}
	Architectures = []Choice{
		{"layered", "Layered (handler / service / repository)"},
		{"clean", "Clean Architecture (domain / usecase / delivery)"},
		{"hexagonal", "Hexagonal (core / ports / adapters)"},
	}
	Databases = []Choice{
		{"postgresql", "PostgreSQL"},
		{"mysql", "MySQL"},
		{"none", "None"},
	}
	Extras = []Choice{
		{"docker", "Dockerfile"},
		{"docker-compose", "docker-compose.yml (app + database)"},
		{"gitlab-ci", "GitLab CI"},
		{"github-actions", "GitHub Actions"},
		{"swagger", "OpenAPI (Swagger) spec"},
	}
)

// Options is the full set of choices for one generated project.
type Options struct {
	Name         string   `yaml:"name"`
	Module       string   `yaml:"module"`
	Language     string   `yaml:"language"`
	Framework    string   `yaml:"framework"`
	Architecture string   `yaml:"architecture"`
	Database     string   `yaml:"database"`
	Extras       []string `yaml:"extras"`
}

// HasExtra reports whether the extra was selected.
func (o Options) HasExtra(name string) bool { return slices.Contains(o.Extras, name) }

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	moduleRe = regexp.MustCompile(`^[A-Za-z0-9._~\-/]+$`)
)

// Validate returns every problem found, joined, or nil.
func (o Options) Validate() error {
	var errs []error
	if !nameRe.MatchString(o.Name) {
		errs = append(errs, fmt.Errorf("name %q: use 2-63 lowercase letters, digits or '-', starting with a letter", o.Name))
	}
	if o.Module == "" || !moduleRe.MatchString(o.Module) || strings.HasPrefix(o.Module, "/") || strings.HasSuffix(o.Module, "/") {
		errs = append(errs, fmt.Errorf("module %q: not a valid Go module path", o.Module))
	}
	if !has(Languages, o.Language) {
		errs = append(errs, fmt.Errorf("language %q: supported: %s", o.Language, values(Languages)))
	} else if !has(Frameworks[o.Language], o.Framework) {
		errs = append(errs, fmt.Errorf("framework %q: not available for %s; supported: %s", o.Framework, o.Language, values(Frameworks[o.Language])))
	}
	if !has(Architectures, o.Architecture) {
		errs = append(errs, fmt.Errorf("architecture %q: supported: %s", o.Architecture, values(Architectures)))
	}
	if !has(Databases, o.Database) {
		errs = append(errs, fmt.Errorf("database %q: supported: %s", o.Database, values(Databases)))
	}
	seen := map[string]bool{}
	for _, e := range o.Extras {
		if !has(Extras, e) {
			errs = append(errs, fmt.Errorf("extra %q: supported: %s", e, values(Extras)))
		}
		if seen[e] {
			errs = append(errs, fmt.Errorf("extra %q: listed twice", e))
		}
		seen[e] = true
	}
	if o.HasExtra("docker-compose") && !o.HasExtra("docker") {
		errs = append(errs, errors.New("extra docker-compose requires docker (compose builds the Dockerfile)"))
	}
	return errors.Join(errs...)
}

func has(cs []Choice, v string) bool {
	return slices.ContainsFunc(cs, func(c Choice) bool { return c.Value == v })
}

func values(cs []Choice) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Value
	}
	return strings.Join(out, ", ")
}
