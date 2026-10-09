// Package preset loads and saves named sets of project choices, so a team
// can start every service from the same stack with one pick.
//
// Presets come from three places; a later one replaces an earlier one with
// the same name:
//
//  1. built in (compiled into projgen)
//  2. team folders listed in PROJGEN_PRESETS (separated like PATH), for
//     example a cloned team repository or a shared drive
//  3. the user's own folder (UserDir), where "projgen preset save" writes
package preset

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/options"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// Sources of a preset.
const (
	SourceBuiltin = "built-in"
	SourceTeam    = "team"
	SourceUser    = "user"
)

// Preset is a named set of choices. Name and module of the project are
// not part of it; the module is built from ModulePrefix and the project name.
type Preset struct {
	Name         string   `yaml:"name" json:"name"`
	Description  string   `yaml:"description,omitempty" json:"description"`
	Language     string   `yaml:"language" json:"language"`
	Framework    string   `yaml:"framework" json:"framework"`
	Architecture string   `yaml:"architecture" json:"architecture"`
	Database     string   `yaml:"database" json:"database"`
	Extras       []string `yaml:"extras" json:"extras"`
	ModulePrefix string   `yaml:"module_prefix,omitempty" json:"module_prefix"`

	Source string `yaml:"-" json:"source"` // built-in, team or user
	Path   string `yaml:"-" json:"-"`      // file it was read from, if any
}

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)

// Options returns the project options for a project called name.
func (p Preset) Options(name string) options.Options {
	return options.Options{
		Name:         name,
		Module:       options.ModuleFor(p.Language, p.ModulePrefix, name),
		Language:     p.Language,
		Framework:    p.Framework,
		Architecture: p.Architecture,
		Database:     p.Database,
		Extras:       slices.Clone(p.Extras),
	}
}

// Validate checks the name and that the choices form a valid project.
func (p Preset) Validate() error {
	var errs []error
	if !nameRe.MatchString(p.Name) {
		errs = append(errs, fmt.Errorf("preset name %q: use 2-41 lowercase letters, digits or '-'", p.Name))
	}
	if err := p.Options("preset-check").Validate(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Summary is a one-line description of the stack, e.g. "go · gin · clean · postgresql".
func (p Preset) Summary() string {
	s := strings.Join([]string{p.Language, p.Framework, p.Architecture, p.Database}, " · ")
	if len(p.Extras) > 0 {
		s += " · " + strings.Join(p.Extras, ", ")
	}
	return s
}

// FromOptions turns project options into a preset; the project's module
// prefix is kept so new projects land next to it.
func FromOptions(name, description string, o options.Options) Preset {
	prefix := ""
	sep := "/"
	if o.Language == "java" {
		sep = "."
	}
	if i := strings.LastIndex(o.Module, sep); i > 0 {
		prefix = o.Module[:i]
	}
	return Preset{
		Name: name, Description: description, Language: o.Language, Framework: o.Framework,
		Architecture: o.Architecture, Database: o.Database, Extras: slices.Clone(o.Extras), ModulePrefix: prefix,
	}
}

// UserDir is where the user's own presets live.
func UserDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projgen", "presets"), nil
}

// TeamDirs returns the folders listed in PROJGEN_PRESETS.
func TeamDirs() []string {
	var dirs []string
	for _, d := range filepath.SplitList(os.Getenv("PROJGEN_PRESETS")) {
		if d = strings.TrimSpace(d); d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// Load returns every preset sorted by name. Files that cannot be read or
// are invalid are skipped and reported in the returned error; the rest still load.
func Load() ([]Preset, error) {
	byName := map[string]Preset{}
	var errs []error

	entries, _ := fs.ReadDir(builtinFS, "builtin")
	for _, e := range entries {
		b, _ := fs.ReadFile(builtinFS, "builtin/"+e.Name())
		p, err := parse(b)
		if err != nil {
			errs = append(errs, fmt.Errorf("built-in %s: %w", e.Name(), err))
			continue
		}
		p.Source = SourceBuiltin
		byName[p.Name] = p
	}
	for _, dir := range TeamDirs() {
		errs = append(errs, loadDir(dir, SourceTeam, byName)...)
	}
	if dir, err := UserDir(); err == nil {
		errs = append(errs, loadDir(dir, SourceUser, byName)...)
	}

	out := make([]Preset, 0, len(byName))
	for _, p := range byName {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Preset) int { return strings.Compare(a.Name, b.Name) })
	return out, errors.Join(errs...)
}

func loadDir(dir, source string, into map[string]Preset) []error {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return []error{err}
	}
	more, _ := filepath.Glob(filepath.Join(dir, "*.yml"))
	files = append(files, more...)
	var errs []error
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		p, err := parse(b)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
			continue
		}
		p.Source, p.Path = source, f
		into[p.Name] = p
	}
	return errs
}

func parse(b []byte) (Preset, error) {
	var p Preset
	if err := yaml.Unmarshal(b, &p); err != nil {
		return p, err
	}
	return p, p.Validate()
}

// Find returns the preset with the given name.
func Find(name string) (Preset, error) {
	all, loadErr := Load()
	for _, p := range all {
		if p.Name == name {
			return p, nil
		}
	}
	names := make([]string, len(all))
	for i, p := range all {
		names[i] = p.Name
	}
	err := fmt.Errorf("no preset %q; available: %s", name, strings.Join(names, ", "))
	if loadErr != nil {
		err = fmt.Errorf("%w\nsome preset files could not be loaded:\n%w", err, loadErr)
	}
	return Preset{}, err
}

// Save validates p and writes it to the user's folder, returning the file path.
// An existing user preset with the same name is replaced only when force is set.
func Save(p Preset, force bool) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	dir, err := UserDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, p.Name+".yaml")
	if _, err := os.Stat(path); err == nil && !force {
		return path, fmt.Errorf("preset %q already exists at %s (use --force to replace it)", p.Name, path)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, b, 0o644)
}
