package generator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

var testV = versions.Defaults()

// Versions from the official sources must end up in the generated files.
func TestRender_UsesVersions(t *testing.T) {
	v := versions.Defaults()
	v.Go = "1.99"
	v.Modules[versions.Echo] = "v4.99.0"
	v.Modules[versions.Pgx] = "v5.99.0"
	v.Images["postgres"] = "99-alpine"
	v.Actions["actions/checkout"] = "v99"

	files, err := Render(opts("echo", "clean", "postgresql", allExtras...), v)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"go.mod":                   {"go 1.99", versions.Echo + " v4.99.0", versions.Pgx + " v5.99.0"},
		"Dockerfile":               {"golang:1.99-alpine"},
		".gitlab-ci.yml":           {"golang:1.99"},
		"docker-compose.yml":       {"image: postgres:99-alpine"},
		".github/workflows/ci.yml": {"actions/checkout@v99"},
	}
	for file, wants := range checks {
		for _, w := range wants {
			if !strings.Contains(string(files[file]), w) {
				t.Errorf("%s does not contain %q:\n%s", file, w, files[file])
			}
		}
	}
	if strings.Contains(string(files["go.mod"]), versions.Gin) {
		t.Error("go.mod requires gin although echo was chosen")
	}
}

func TestRender_NetHTTPNoDBHasNoRequires(t *testing.T) {
	files, err := Render(opts("nethttp", "layered", "none"), testV)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files["go.mod"]), "require") {
		t.Errorf("go.mod should have no requires:\n%s", files["go.mod"])
	}
}

func opts(fw, arch, db string, extras ...string) options.Options {
	return options.Options{
		Name:         "demo-service",
		Module:       "example.com/demo-service",
		Language:     "go",
		Framework:    fw,
		Architecture: arch,
		Database:     db,
		Extras:       extras,
	}
}

var allExtras = []string{"docker", "docker-compose", "gitlab-ci", "github-actions", "swagger"}

// extrasFor returns every extra valid for the database: migrations need one.
func extrasFor(db string) []string {
	if db == "none" {
		return allExtras
	}
	return append(slices.Clone(allExtras), "migrations")
}

// combos returns every framework x architecture x database combination.
func combos() []options.Options {
	var out []options.Options
	for _, fw := range options.Frameworks["go"] {
		for _, a := range options.Architectures {
			for _, d := range options.Databases {
				out = append(out, opts(fw.Value, a.Value, d.Value, extrasFor(d.Value)...))
			}
		}
	}
	return out
}

func name(o options.Options) string { return o.Framework + "-" + o.Architecture + "-" + o.Database }

// Every combination renders, and every .go file is valid, formatted Go.
func TestRender_AllCombinations(t *testing.T) {
	for _, o := range combos() {
		t.Run(name(o), func(t *testing.T) {
			files, err := Render(o, testV)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			d := NewData(o, testV)
			want := []string{
				"go.mod", "cmd/server/main.go", "README.md", "Makefile", ".gitignore", ".env.example", "project.yaml",
				d.P["http"].Dir + "/router.go", d.P["http"].Dir + "/router_test.go",
				d.P["service"].Dir + "/greeter.go", d.P["logger"].Dir + "/logger.go",
				"Dockerfile", "docker-compose.yml", ".gitlab-ci.yml", ".github/workflows/ci.yml", "docs/openapi.yaml",
			}
			if o.Database != "none" {
				want = append(want, d.P["db"].Dir+"/db.go", d.P["db"].Dir+"/migrate.go", "migrations/migrations.go")
				// The first migration is named after the generation time, which
				// Render reads itself; match the shape, not this test's clock.
				initRe := regexp.MustCompile(`^migrations/\d{14}_init\.(up|down)\.sql$`)
				n := 0
				for p := range files {
					if initRe.MatchString(p) {
						n++
					}
				}
				if n != 2 {
					t.Errorf("found %d init migration files, want up and down", n)
				}
				if !strings.Contains(string(files["cmd/server/main.go"]), ".Migrate(cfg.DatabaseURL)") {
					t.Error("main.go does not run migrations")
				}
				if !strings.Contains(string(files["go.mod"]), versions.Migrate) {
					t.Error("go.mod does not require golang-migrate")
				}
			}
			for _, w := range want {
				if _, ok := files[w]; !ok {
					t.Errorf("missing %s", w)
				}
			}
			for p, b := range files {
				if strings.Contains(string(b), "<no value>") {
					t.Errorf("%s contains <no value>: a template field is missing", p)
				}
			}
			main := string(files["cmd/server/main.go"])
			if !strings.Contains(main, d.P["http"].Import) {
				t.Errorf("main.go does not import the http package %s", d.P["http"].Import)
			}
			if (o.Database != "none") != strings.Contains(main, "DatabaseURL") {
				t.Errorf("main.go database wiring does not match database=%s", o.Database)
			}
		})
	}
}

func TestRender_ExtrasOnlyWhenChosen(t *testing.T) {
	files, err := Render(opts("gin", "layered", "none"), testV)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Dockerfile", "docker-compose.yml", ".gitlab-ci.yml", ".github/workflows/ci.yml", "docs/openapi.yaml", "internal/repository/db.go"} {
		if _, ok := files[p]; ok {
			t.Errorf("%s generated but not chosen", p)
		}
	}
}

func TestRender_InvalidOptions(t *testing.T) {
	if _, err := Render(opts("spring-boot", "clean", "postgresql"), testV); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRender_ProjectYAMLRoundTrip(t *testing.T) {
	o := opts("echo", "hexagonal", "mysql", "docker")
	files, err := Render(o, testV)
	if err != nil {
		t.Fatal(err)
	}
	var back options.Options
	if err := yaml.Unmarshal(files["project.yaml"], &back); err != nil {
		t.Fatal(err)
	}
	if back.Framework != o.Framework || back.Architecture != o.Architecture || back.Database != o.Database || len(back.Extras) != 1 {
		t.Errorf("round trip mismatch: %+v", back)
	}
}

func TestGenerate_WritesFiles(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(opts("nethttp", "clean", "postgresql"), testV, dir, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("listed file not written: %s", f)
		}
	}
}

func TestGenerate_DryRunWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	res, err := Generate(opts("gin", "layered", "none"), testV, dir, Flags{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		t.Error("dry run listed no files")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dry run created %s", dir)
	}
}

func TestGenerate_NonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Generate(opts("gin", "layered", "none"), testV, dir, Flags{})
	if !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("err = %v, want ErrDirNotEmpty", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); !os.IsNotExist(err) {
		t.Error("files written despite refusal")
	}

	if _, err := Generate(opts("gin", "layered", "none"), testV, dir, Flags{Force: true}); err != nil {
		t.Fatalf("force: %v", err)
	}
	if b, _ := os.ReadFile(keep); string(b) != "mine" {
		t.Error("force touched an unrelated file")
	}
}

// TestGenerated_BuildAndTest generates every combination, downloads
// dependencies and runs go vet + go test on the result. It needs network
// access and takes minutes, so it runs only with PROJGEN_E2E=1.
//
// With PROJGEN_E2E_LATEST=1 it first fetches the latest versions from the
// official sources, so a new release that breaks the templates is caught
// before developers hit it. Any source that cannot be reached fails the test.
func TestGenerated_BuildAndTest(t *testing.T) {
	if os.Getenv("PROJGEN_E2E") != "1" {
		t.Skip("set PROJGEN_E2E=1 to build and test generated projects")
	}
	v := testV
	if os.Getenv("PROJGEN_E2E_LATEST") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		got, err := versions.NewFetcher().Fetch(ctx)
		if err != nil {
			t.Fatalf("fetching latest versions: %v", err)
		}
		v = versions.Merge(testV, got)
	}
	t.Logf("versions: go %s, modules %v, images %v, actions %v", v.Go, v.Modules, v.Images, v.Actions)

	for _, o := range combos() {
		t.Run(name(o), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Generate(o, v, dir, Flags{}); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"mod", "tidy"}, {"vet", "./..."}, {"test", "./..."}} {
				cmd := exec.Command("go", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
		})
	}
}
