package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testConfig = `name: demo-api
module: github.com/MyagmardorjD/demo-api
language: go
framework: gin
architecture: layered
database: none
`

func TestNew_PrintsAbsoluteLocation(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "project.yaml")
	if err := os.WriteFile(cfg, []byte(testConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmp)

	var out bytes.Buffer
	if err := run([]string{"new", "--config", cfg, "--skip-tidy", "--no-git"}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}

	want, err := filepath.Abs("demo-api")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Project location: "+want) {
		t.Errorf("output does not show absolute location %s:\n%s", want, out.String())
	}
	if _, err := os.Stat(filepath.Join(want, "go.mod")); err != nil {
		t.Errorf("project not created at %s: %v", want, err)
	}
}

func TestNew_DryRunShowsAbsoluteLocation(t *testing.T) {
	tmp := t.TempDir()
	cfg := filepath.Join(tmp, "project.yaml")
	if err := os.WriteFile(cfg, []byte(testConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(tmp)

	var out bytes.Buffer
	if err := run([]string{"new", "--config", cfg, "--dry-run"}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	want, _ := filepath.Abs("demo-api")
	if !strings.Contains(out.String(), want) {
		t.Errorf("dry run output does not show %s:\n%s", want, out.String())
	}
}
