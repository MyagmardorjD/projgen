package create

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

func goOpts() options.Options {
	return options.Options{Name: "shop", Module: "example.com/shop", Language: "go",
		Framework: "nethttp", Architecture: "layered", Database: "none"}
}

func javaOpts() options.Options {
	return options.Options{Name: "shop", Module: "com.techpartners.shop", Language: "java",
		Framework: "spring-boot", Architecture: "layered", Database: "none"}
}

func TestDefaultParent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := DefaultParent(), filepath.Join(home, "source", "repos"); got != want {
		t.Errorf("DefaultParent() = %q, want %q", got, want)
	}
}

func TestProject_DryRunWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shop")
	var out bytes.Buffer
	res, warns, err := Project(goOpts(), versions.Defaults(), dir, generator.Flags{DryRun: true}, Steps{Tidy: true, Git: true}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 || len(warns) != 0 {
		t.Errorf("files = %d, warnings = %v; want files and no warnings", len(res.Files), warns)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dry run created %s", dir)
	}
	if out.Len() != 0 {
		t.Errorf("dry run ran commands: %s", out.String())
	}
}

func TestProject_NoSteps(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	res, warns, err := Project(goOpts(), versions.Defaults(), dir, generator.Flags{}, Steps{}, &out)
	if err != nil || len(warns) != 0 {
		t.Fatalf("err = %v, warnings = %v", err, warns)
	}
	for _, f := range res.Files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("listed file not written: %s", f)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Error("git init ran although Git was off")
	}
}

func TestProject_NonEmptyDirIsAnError(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o644)
	_, _, err := Project(goOpts(), versions.Defaults(), dir, generator.Flags{}, Steps{}, &bytes.Buffer{})
	if !errors.Is(err, generator.ErrDirNotEmpty) {
		t.Fatalf("err = %v, want ErrDirNotEmpty", err)
	}
}

func TestProject_InvalidOptions(t *testing.T) {
	o := goOpts()
	o.Framework = "rails"
	if _, _, err := Project(o, versions.Defaults(), t.TempDir(), generator.Flags{}, Steps{}, &bytes.Buffer{}); err == nil {
		t.Fatal("want a validation error")
	}
}

func TestProject_GitInit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	_, warns, err := Project(goOpts(), versions.Defaults(), dir, generator.Flags{}, Steps{Git: true}, &bytes.Buffer{})
	if err != nil || len(warns) != 0 {
		t.Fatalf("err = %v, warnings = %v", err, warns)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("no .git after git init: %v", err)
	}

	// A second run with --force keeps the existing repository.
	_, warns, err = Project(goOpts(), versions.Defaults(), dir, generator.Flags{Force: true}, Steps{Git: true}, &bytes.Buffer{})
	if err != nil || len(warns) != 0 {
		t.Fatalf("second run: err = %v, warnings = %v", err, warns)
	}
}

func TestProject_JavaSkipsTidy(t *testing.T) {
	// PATH without go: tidy would fail with a warning if it ran.
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	_, warns, err := Project(javaOpts(), versions.Defaults(), dir, generator.Flags{}, Steps{Tidy: true}, &bytes.Buffer{})
	if err != nil || len(warns) != 0 {
		t.Fatalf("err = %v, warnings = %v; want no tidy for Java", err, warns)
	}
}

func TestProject_FailedStepIsAWarning(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // neither go nor git can be found
	dir := t.TempDir()
	res, warns, err := Project(goOpts(), versions.Defaults(), dir, generator.Flags{}, Steps{Tidy: true, Git: true}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("a failed step must not be an error: %v", err)
	}
	if len(res.Files) == 0 {
		t.Error("project files missing")
	}
	var steps []string
	for _, w := range warns {
		steps = append(steps, w.Step)
	}
	if got := strings.Join(steps, ","); got != "go mod tidy,git init" {
		t.Errorf("warnings = %s, want go mod tidy,git init", got)
	}
}
