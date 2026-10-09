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
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

// templates/ holds text/template files; static/ holds files copied as-is
// (the official Maven Wrapper scripts).
//
//go:embed all:templates all:static
var templateFS embed.FS

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

// Pkg is one package in the generated project.
type Pkg struct {
	Dir     string // slash-separated, relative to the project root
	Name    string // Go: package name. Java: full package name
	Import  string // Go: import path. Java: full package name
	TestDir string // Java only: where the package's tests live
}

// DB holds database-specific values used by templates.
type DB struct {
	Driver     string // database/sql driver name
	Module     string // driver module path
	Import     string // driver import path
	Image      string // docker image name; the tag comes from versions
	Port       string
	LocalURL   string // DSN for running the app on the host
	ComposeURL string // DSN inside docker-compose

	JavaGroup      string // JDBC driver Maven groupId
	JavaArtifact   string // JDBC driver Maven artifactId
	JavaLocalURL   string // JDBC URL for running the app on the host
	JavaComposeURL string // JDBC URL inside docker-compose
}

// Require is one line of the generated go.mod require block.
type Require struct {
	Path    string
	Version string
}

// Data is passed to every template.
type Data struct {
	Opt      options.Options
	P        map[string]Pkg
	HasDB    bool
	DB       DB
	V        versions.Versions
	Requires []Require

	Migrations bool   // the migrations extra is chosen
	Stamp      string // generation time, used to name the first migration

	Java    bool   // language is java
	BasePkg string // Java base package (module)
	BaseDir string // Java: src/main/java/<base package path>
	App     string // Java main class, e.g. OrderServiceApplication
}

// frameworkModules maps a framework to the Go module it needs.
var frameworkModules = map[string]string{
	"gin":   versions.Gin,
	"echo":  versions.Echo,
	"fiber": versions.Fiber,
}

type file struct {
	out    string // output path, slash-separated
	tmpl   string // template path under templates/, or under static/ when static
	static bool   // copy as-is, no template processing
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

// javaLayouts maps architecture -> component -> package suffix under the base package.
var javaLayouts = map[string]map[string]string{
	"layered":   {"config": "config", "domain": "model", "service": "service", "http": "controller", "db": "repository"},
	"clean":     {"config": "config", "domain": "domain", "service": "usecase", "http": "delivery.web", "db": "repository"},
	"hexagonal": {"config": "config", "domain": "core.domain", "service": "core.service", "http": "adapters.web", "db": "adapters.persistence"},
}

var databases = map[string]DB{
	"postgresql": {
		Driver:     "pgx",
		Module:     versions.Pgx,
		Import:     versions.Pgx + "/stdlib",
		Image:      "postgres",
		Port:       "5432",
		LocalURL:   "postgres://app:app@localhost:5432/app?sslmode=disable",
		ComposeURL: "postgres://${DB_USER:-app}:${DB_PASSWORD:-app}@db:5432/${DB_NAME:-app}?sslmode=disable",

		JavaGroup:      "org.postgresql",
		JavaArtifact:   "postgresql",
		JavaLocalURL:   "jdbc:postgresql://localhost:5432/app",
		JavaComposeURL: "jdbc:postgresql://db:5432/${DB_NAME:-app}",
	},
	"mysql": {
		Driver:     "mysql",
		Module:     versions.MySQL,
		Import:     versions.MySQL,
		Image:      "mysql",
		Port:       "3306",
		LocalURL:   "app:app@tcp(localhost:3306)/app?parseTime=true",
		ComposeURL: "${DB_USER:-app}:${DB_PASSWORD:-app}@tcp(db:3306)/${DB_NAME:-app}?parseTime=true",

		JavaGroup:      "com.mysql",
		JavaArtifact:   "mysql-connector-j",
		JavaLocalURL:   "jdbc:mysql://localhost:3306/app",
		JavaComposeURL: "jdbc:mysql://db:3306/${DB_NAME:-app}",
	},
}

// NewData builds template data for valid options and the given versions.
func NewData(o options.Options, v versions.Versions) Data {
	if o.Language == "java" {
		return newJavaData(o, v)
	}
	dbShort := map[string]string{"postgresql": "postgres", "mysql": "mysql", "none": "memory"}[o.Database]
	p := map[string]Pkg{}
	for comp, v := range layouts[o.Architecture] {
		dir := strings.ReplaceAll(v[0], "{db}", dbShort)
		name := strings.ReplaceAll(v[1], "{db}", dbShort)
		p[comp] = Pkg{Dir: dir, Name: name, Import: o.Module + "/" + dir}
	}
	d := Data{Opt: o, P: p, HasDB: o.Database != "none", DB: databases[o.Database], V: v,
		Migrations: o.HasExtra("migrations"), Stamp: time.Now().UTC().Format("20060102150405")}
	if m, ok := frameworkModules[o.Framework]; ok {
		d.Requires = append(d.Requires, Require{m, v.Modules[m]})
	}
	if d.HasDB {
		d.DB.Image += ":" + v.Images[d.DB.Image]
		d.Requires = append(d.Requires, Require{d.DB.Module, v.Modules[d.DB.Module]})
	}
	if d.Migrations {
		d.Requires = append(d.Requires, Require{versions.Migrate, v.Modules[versions.Migrate]})
	}
	return d
}

func newJavaData(o options.Options, v versions.Versions) Data {
	baseDir := "src/main/java/" + strings.ReplaceAll(o.Module, ".", "/")
	testDir := "src/test/java/" + strings.ReplaceAll(o.Module, ".", "/")
	p := map[string]Pkg{}
	for comp, suffix := range javaLayouts[o.Architecture] {
		sub := strings.ReplaceAll(suffix, ".", "/")
		pkg := o.Module + "." + suffix
		p[comp] = Pkg{Dir: baseDir + "/" + sub, Name: pkg, Import: pkg, TestDir: testDir + "/" + sub}
	}
	d := Data{
		Opt: o, P: p, HasDB: o.Database != "none", DB: databases[o.Database], V: v,
		Migrations: o.HasExtra("migrations"),
		Java:       true, BasePkg: o.Module, BaseDir: baseDir, App: className(o.Name) + "Application",
	}
	if d.HasDB {
		d.DB.Image += ":" + v.Images[d.DB.Image]
	}
	return d
}

// className turns "order-service" into "OrderService".
func className(name string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' }) {
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	s := b.String()
	if s == "" || s[0] >= '0' && s[0] <= '9' {
		s = "App" + s
	}
	return s
}

func javaPlan(d Data) []file {
	o, p := d.Opt, d.P
	testBase := strings.Replace(d.BaseDir, "src/main/", "src/test/", 1)
	files := []file{
		{"pom.xml", "java/pom.xml.tmpl", false},
		{"mvnw", "java/mvnw", true},
		{"mvnw.cmd", "java/mvnw.cmd", true},
		{".mvn/wrapper/maven-wrapper.properties", "java/maven-wrapper.properties.tmpl", false},
		{".gitattributes", "java/gitattributes.tmpl", false},
		{".gitignore", "java/gitignore.tmpl", false},
		{".env.example", "env.example.tmpl", false},
		{"README.md", "java/README.md.tmpl", false},
		{d.BaseDir + "/" + d.App + ".java", "java/Application.java.tmpl", false},
		{p["domain"].Dir + "/Greeting.java", "java/Greeting.java.tmpl", false},
		{p["domain"].Dir + "/NameTooLongException.java", "java/NameTooLongException.java.tmpl", false},
		{p["service"].Dir + "/GreeterService.java", "java/GreeterService.java.tmpl", false},
		{p["http"].Dir + "/HelloController.java", "java/HelloController.java.tmpl", false},
		{p["http"].Dir + "/HealthController.java", "java/HealthController.java.tmpl", false},
		{p["http"].Dir + "/RequestIdFilter.java", "java/RequestIdFilter.java.tmpl", false},
		{p["http"].Dir + "/ApiExceptionHandler.java", "java/ApiExceptionHandler.java.tmpl", false},
		{"src/main/resources/application.yml", "java/application.yml.tmpl", false},
		{"src/main/resources/log4j2.xml", "java/log4j2.xml.tmpl", false},
		{"src/main/resources/log-event-template.json", "java/log-event-template.json.tmpl", false},
		{testBase + "/" + d.App + "Tests.java", "java/ApplicationTests.java.tmpl", false},
		{p["service"].TestDir + "/GreeterServiceTest.java", "java/GreeterServiceTest.java.tmpl", false},
		{p["http"].TestDir + "/HelloControllerTest.java", "java/HelloControllerTest.java.tmpl", false},
		{p["http"].TestDir + "/HealthControllerTest.java", "java/HealthControllerTest.java.tmpl", false},
	}
	if d.Migrations {
		files = append(files,
			file{"src/main/resources/db/migration/V1__init.sql", "migrations/init.up.sql.tmpl", false},
			file{p["db"].TestDir + "/MigrationTests.java", "java/MigrationTests.java.tmpl", false},
		)
	}
	extras := map[string]file{
		"docker":         {"Dockerfile", "java/Dockerfile.tmpl", false},
		"docker-compose": {"docker-compose.yml", "docker-compose.yml.tmpl", false},
		"gitlab-ci":      {".gitlab-ci.yml", "java/gitlab-ci.yml.tmpl", false},
		"github-actions": {".github/workflows/ci.yml", "java/github-ci.yml.tmpl", false},
		// swagger: springdoc serves the spec at /v3/api-docs; it is added in pom.xml.
	}
	for _, e := range o.Extras {
		if f, ok := extras[e]; ok {
			files = append(files, f)
		}
	}
	return files
}

func plan(d Data) []file {
	if d.Java {
		return javaPlan(d)
	}
	o, p := d.Opt, d.P
	files := []file{
		{"go.mod", "go.mod.tmpl", false},
		{"cmd/server/main.go", "main.go.tmpl", false},
		{p["config"].Dir + "/config.go", "config.go.tmpl", false},
		{p["config"].Dir + "/config_test.go", "config_test.go.tmpl", false},
		{p["logger"].Dir + "/logger.go", "logger.go.tmpl", false},
		{p["logger"].Dir + "/logger_test.go", "logger_test.go.tmpl", false},
		{p["domain"].Dir + "/greeting.go", "greeting.go.tmpl", false},
		{p["service"].Dir + "/greeter.go", "greeter.go.tmpl", false},
		{p["service"].Dir + "/greeter_test.go", "greeter_test.go.tmpl", false},
		{p["http"].Dir + "/router.go", "http/" + o.Framework + ".go.tmpl", false},
		{p["http"].Dir + "/router_test.go", "http/router_test.go.tmpl", false},
		{".gitignore", "gitignore.tmpl", false},
		{".env.example", "env.example.tmpl", false},
		{"Makefile", "Makefile.tmpl", false},
		{"README.md", "README.md.tmpl", false},
	}
	if d.HasDB {
		files = append(files, file{p["db"].Dir + "/db.go", "db.go.tmpl", false})
	}
	if d.Migrations {
		files = append(files,
			file{"migrations/" + d.Stamp + "_init.up.sql", "migrations/init.up.sql.tmpl", false},
			file{"migrations/" + d.Stamp + "_init.down.sql", "migrations/init.down.sql.tmpl", false},
			file{"migrations/migrations.go", "migrations/migrations.go.tmpl", false},
			file{p["db"].Dir + "/migrate.go", "migrations/migrate.go.tmpl", false},
			file{p["db"].Dir + "/migrate_test.go", "migrations/migrate_test.go.tmpl", false},
		)
	}
	extras := map[string]file{
		"docker":         {"Dockerfile", "Dockerfile.tmpl", false},
		"docker-compose": {"docker-compose.yml", "docker-compose.yml.tmpl", false},
		"gitlab-ci":      {".gitlab-ci.yml", "gitlab-ci.yml.tmpl", false},
		"github-actions": {".github/workflows/ci.yml", "github-ci.yml.tmpl", false},
		"swagger":        {"docs/openapi.yaml", "openapi.yaml.tmpl", false},
	}
	for _, e := range o.Extras {
		if f, ok := extras[e]; ok { // migrations has no single file of its own
			files = append(files, f)
		}
	}
	return files
}

// Render validates the options and renders every file in memory using the
// given technology versions. Keys are slash-separated paths relative to the
// project root.
func Render(o options.Options, v versions.Versions) (map[string][]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	d := NewData(o, v)
	out := map[string][]byte{}
	for _, f := range plan(d) {
		if f.static {
			b, err := fs.ReadFile(templateFS, path.Join("static", f.tmpl))
			if err != nil {
				return nil, err
			}
			out[f.out] = b
			continue
		}
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

// Generate renders the project with the given versions and writes it to dir.
func Generate(o options.Options, v versions.Versions, dir string, fl Flags) (Result, error) {
	files, err := Render(o, v)
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
		mode := os.FileMode(0o644)
		if path.Base(p) == "mvnw" {
			mode = 0o755 // the Maven Wrapper must be executable
		}
		if err := os.WriteFile(full, files[p], mode); err != nil {
			return res, err
		}
	}
	return res, nil
}
