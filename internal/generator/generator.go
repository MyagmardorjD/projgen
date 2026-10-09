// Package generator renders the embedded templates for a set of options and
// writes the resulting project to disk.
package generator

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/options"
)

//go:embed all:templates
var templateFS embed.FS

// GoVersion is the go directive written to generated go.mod files.
const GoVersion = "1.24"

// ErrDirNotEmpty is returned when the target exists, is not empty and Force is off.
var ErrDirNotEmpty = errors.New("target directory is not empty")

// Flags change how files are written.
type Flags struct {
	DryRun bool // render and list files, write nothing
	Force  bool // write into a non-empty directory, overwriting files
}

// Result lists the files that were (or, in a dry run, would be) written.
type Result struct {
	Dir   string
	Files []string
}

// Pkg is one Go package in the generated project.
type Pkg struct {
	Dir    string // slash-separated, relative to the project root
	Name   string // package name
	Import string // full import path
}

// DB holds database-specific values used by templates.
type DB struct {
	Driver     string // database/sql driver name
	Import     string // driver import path
	Image      string // docker image
	Port       string
	LocalURL   string // DSN for running the app on the host
	ComposeURL string // DSN inside docker-compose
}

// Data is passed to every template.
type Data struct {
	Opt       options.Options
	P         map[string]Pkg
	HasDB     bool
	DB        DB
	GoVersion string
}

type file struct {
	out  string // output path, slash-separated
	tmpl string // template path under templates/
}

// layouts maps architecture -> component -> {dir, package name}.
var layouts = map[string]map[string][2]string{
	"layered": {
		"config":  {"internal/config", "config"},
		"logger":  {"internal/logger", "logger"},
		"domain":  {"internal/model", "model"},
		"service": {"internal/service", "service"},
		"http":    {"internal/handler", "handler"},
		"db":      {"internal/repository", "repository"},
	},
	"clean": {
		"config":  {"config", "config"},
		"logger":  {"pkg/logger", "logger"},
		"domain":  {"internal/domain", "domain"},
		"service": {"internal/usecase", "usecase"},
		"http":    {"internal/delivery/http", "delivery"},
		"db":      {"internal/repository/{db}", "{db}"},
	},
	"hexagonal": {
		"config":  {"internal/config", "config"},
		"logger":  {"pkg/logger", "logger"},
		"domain":  {"internal/core/domain", "domain"},
		"service": {"internal/core/services", "services"},
		"http":    {"internal/adapters/http", "httpadapter"},
		"db":      {"internal/adapters/db", "dbadapter"},
	},
}

var databases = map[string]DB{
	"postgresql": {
		Driver:     "pgx",
		Import:     "github.com/jackc/pgx/v5/stdlib",
		Image:      "postgres:17-alpine",
		Port:       "5432",
		LocalURL:   "postgres://app:app@localhost:5432/app?sslmode=disable",
		ComposeURL: "postgres://${DB_USER:-app}:${DB_PASSWORD:-app}@db:5432/${DB_NAME:-app}?sslmode=disable",
	},
	"mysql": {
		Driver:     "mysql",
		Import:     "github.com/go-sql-driver/mysql",
		Image:      "mysql:8.4",
		Port:       "3306",
		LocalURL:   "app:app@tcp(localhost:3306)/app?parseTime=true",
		ComposeURL: "${DB_USER:-app}:${DB_PASSWORD:-app}@tcp(db:3306)/${DB_NAME:-app}?parseTime=true",
	},
}

// NewData builds template data for valid options.
func NewData(o options.Options) Data {
	dbShort := map[string]string{"postgresql": "postgres", "mysql": "mysql"}[o.Database]
	p := map[string]Pkg{}
	for comp, v := range layouts[o.Architecture] {
		dir := strings.ReplaceAll(v[0], "{db}", dbShort)
		name := strings.ReplaceAll(v[1], "{db}", dbShort)
		p[comp] = Pkg{Dir: dir, Name: name, Import: o.Module + "/" + dir}
	}
	return Data{Opt: o, P: p, HasDB: o.Database != "none", DB: databases[o.Database], GoVersion: GoVersion}
}

func plan(d Data) []file {
	o, p := d.Opt, d.P
	files := []file{
		{"go.mod", "go.mod.tmpl"},
		{"cmd/server/main.go", "main.go.tmpl"},
		{p["config"].Dir + "/config.go", "config.go.tmpl"},
		{p["config"].Dir + "/config_test.go", "config_test.go.tmpl"},
		{p["logger"].Dir + "/logger.go", "logger.go.tmpl"},
		{p["logger"].Dir + "/logger_test.go", "logger_test.go.tmpl"},
		{p["domain"].Dir + "/greeting.go", "greeting.go.tmpl"},
		{p["service"].Dir + "/greeter.go", "greeter.go.tmpl"},
		{p["service"].Dir + "/greeter_test.go", "greeter_test.go.tmpl"},
		{p["http"].Dir + "/router.go", "http/" + o.Framework + ".go.tmpl"},
		{p["http"].Dir + "/router_test.go", "http/router_test.go.tmpl"},
		{".gitignore", "gitignore.tmpl"},
		{".env.example", "env.example.tmpl"},
		{"Makefile", "Makefile.tmpl"},
		{"README.md", "README.md.tmpl"},
	}
	if d.HasDB {
		files = append(files, file{p["db"].Dir + "/db.go", "db.go.tmpl"})
	}
	extras := map[string]file{
		"docker":         {"Dockerfile", "Dockerfile.tmpl"},
		"docker-compose": {"docker-compose.yml", "docker-compose.yml.tmpl"},
		"gitlab-ci":      {".gitlab-ci.yml", "gitlab-ci.yml.tmpl"},
		"github-actions": {".github/workflows/ci.yml", "github-ci.yml.tmpl"},
		"swagger":        {"docs/openapi.yaml", "openapi.yaml.tmpl"},
	}
	for _, e := range o.Extras {
		files = append(files, extras[e])
	}
	return files
}

// Render validates the options and renders every file in memory.
// Keys are slash-separated paths relative to the project root.
func Render(o options.Options) (map[string][]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	d := NewData(o)
	out := map[string][]byte{}
	for _, f := range plan(d) {
		b, err := renderOne(f.tmpl, d)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(f.out, ".go") {
			if b, err = format.Source(b); err != nil {
				return nil, fmt.Errorf("template %s produced invalid Go: %w", f.tmpl, err)
			}
		}
		out[f.out] = b
	}
	cfg, err := yaml.Marshal(o)
	if err != nil {
		return nil, err
	}
	out["project.yaml"] = cfg
	return out, nil
}

func renderOne(name string, d Data) ([]byte, error) {
	src, err := fs.ReadFile(templateFS, path.Join("templates", name))
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, d); err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

// Generate renders the project and writes it to dir.
func Generate(o options.Options, dir string, fl Flags) (Result, error) {
	files, err := Render(o)
	if err != nil {
		return Result{}, err
	}
	res := Result{Dir: dir}
	for p := range files {
		res.Files = append(res.Files, p)
	}
	slices.Sort(res.Files)

	if !fl.Force {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
			return res, fmt.Errorf("%w: %s (use --force to write anyway)", ErrDirNotEmpty, dir)
		}
	}
	if fl.DryRun {
		return res, nil
	}
	for _, p := range res.Files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(full, files[p], 0o644); err != nil {
			return res, err
		}
	}
	return res, nil
}
