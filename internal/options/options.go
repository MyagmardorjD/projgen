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
	Value string `json:"value"`
	Label string `json:"label"`
}

var (
	Languages = []Choice{
		{"go", "Go"},
		{"java", "Java"},
	}
	Frameworks = map[string][]Choice{
		"go": {
			{"gin", "Gin"},
			{"echo", "Echo"},
			{"fiber", "Fiber"},
			{"nethttp", "net/http (standard library)"},
		},
		"java": {
			{"spring-boot", "Spring Boot (Maven, Log4j 2 JSON)"},
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
		{"migrations", "DB migrations on start (golang-migrate / Flyway)"},
	}
)

// Options is the full set of choices for one generated project.
type Options struct {
	Name         string   `yaml:"name" json:"name"`
	Module       string   `yaml:"module" json:"module"`
	Language     string   `yaml:"language" json:"language"`
	Framework    string   `yaml:"framework" json:"framework"`
	Architecture string   `yaml:"architecture" json:"architecture"`
	Database     string   `yaml:"database" json:"database"`
	Extras       []string `yaml:"extras" json:"extras"`
}

// HasExtra reports whether the extra was selected.
func (o Options) HasExtra(name string) bool { return slices.Contains(o.Extras, name) }

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	moduleRe  = regexp.MustCompile(`^[A-Za-z0-9._~\-/]+$`)
	javaPkgRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	javaWords = []string{"abstract", "boolean", "byte", "case", "catch", "char", "class", "const", "default", "do", "double",
		"else", "enum", "extends", "final", "float", "for", "goto", "if", "import", "int", "interface", "long", "new",
		"package", "private", "protected", "public", "return", "short", "static", "super", "switch", "this", "throw",
		"try", "void", "while", "true", "false", "null"}
)

// DefaultModule suggests a module (Go) or base package (Java) for a project name.
func DefaultModule(language, name string) string { return ModuleFor(language, "", name) }

// ModuleFor builds a module (Go: prefix/name) or base package (Java:
// prefix.name) from a prefix. An empty prefix uses the default one.
func ModuleFor(language, prefix, name string) string {
	prefix = strings.Trim(prefix, "./ ")
	if language == "java" {
		if prefix == "" {
			prefix = "com.techpartners"
		}
		pkg := strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(name, ""))
		if pkg == "" || pkg[0] >= '0' && pkg[0] <= '9' {
			pkg = "app" + pkg
		}
		return prefix + "." + pkg
	}
	if prefix == "" {
		prefix = "github.com/MyagmardorjD"
	}
	return prefix + "/" + name
}

func validModule(language, m string) error {
	if language == "java" {
		if !javaPkgRe.MatchString(m) {
			return fmt.Errorf("module %q: for Java use a base package like com.techpartners.orderservice", m)
		}
		for _, part := range strings.Split(m, ".") {
			if slices.Contains(javaWords, part) {
				return fmt.Errorf("module %q: %q is a Java keyword", m, part)
			}
		}
		return nil
	}
	if m == "" || !moduleRe.MatchString(m) || strings.HasPrefix(m, "/") || strings.HasSuffix(m, "/") {
		return fmt.Errorf("module %q: not a valid Go module path", m)
	}
	return nil
}

// Validate returns every problem found, joined, or nil.
func (o Options) Validate() error {
	var errs []error
	if !nameRe.MatchString(o.Name) {
		errs = append(errs, fmt.Errorf("name %q: use 2-63 lowercase letters, digits or '-', starting with a letter", o.Name))
	}
	if err := validModule(o.Language, o.Module); err != nil {
		errs = append(errs, err)
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
	if o.HasExtra("migrations") && o.Database == "none" {
		errs = append(errs, errors.New("extra migrations requires a database"))
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
