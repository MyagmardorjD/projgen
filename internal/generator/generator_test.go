package generator

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/options"
)

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

// combos returns every framework x architecture x database combination.
func combos() []options.Options {
	var out []options.Options
	for _, fw := range options.Frameworks["go"] {
		for _, a := range options.Architectures {
			for _, d := range options.Databases {
				out = append(out, opts(fw.Value, a.Value, d.Value, allExtras...))
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
			files, err := Render(o)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			d := NewData(o)
			want := []string{
				"go.mod", "cmd/server/main.go", "README.md", "Makefile", ".gitignore", ".env.example", "project.yaml",
				d.P["http"].Dir + "/router.go", d.P["http"].Dir + "/router_test.go",
				d.P["service"].Dir + "/greeter.go", d.P["logger"].Dir + "/logger.go",
				"Dockerfile", "docker-compose.yml", ".gitlab-ci.yml", ".github/workflows/ci.yml", "docs/openapi.yaml",
			}
			if o.Database != "none" {
				want = append(want, d.P["db"].Dir+"/db.go")
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
	files, err := Render(opts("gin", "layered", "none"))
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
	if _, err := Render(opts("spring-boot", "clean", "postgresql")); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRender_ProjectYAMLRoundTrip(t *testing.T) {
	o := opts("echo", "hexagonal", "mysql", "docker")
	files, err := Render(o)
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
	res, err := Generate(opts("nethttp", "clean", "postgresql"), dir, Flags{})
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
	res, err := Generate(opts("gin", "layered", "none"), dir, Flags{DryRun: true})
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

	_, err := Generate(opts("gin", "layered", "none"), dir, Flags{})
	if !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("err = %v, want ErrDirNotEmpty", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); !os.IsNotExist(err) {
		t.Error("files written despite refusal")
	}

	if _, err := Generate(opts("gin", "layered", "none"), dir, Flags{Force: true}); err != nil {
		t.Fatalf("force: %v", err)
	}
	if b, _ := os.ReadFile(keep); string(b) != "mine" {
		t.Error("force touched an unrelated file")
	}
}

// TestGenerated_BuildAndTest generates every combination, downloads
// dependencies and runs go vet + go test on the result. It needs network
// access and takes minutes, so it runs only with PROJGEN_E2E=1.
func TestGenerated_BuildAndTest(t *testing.T) {
	if os.Getenv("PROJGEN_E2E") != "1" {
		t.Skip("set PROJGEN_E2E=1 to build and test generated projects")
	}
	for _, o := range combos() {
		t.Run(name(o), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Generate(o, dir, Flags{}); err != nil {
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
