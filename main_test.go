package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MyagmardorjD/projgen/internal/versions"
)

const testConfig = `name: demo-api
module: github.com/MyagmardorjD/demo-api
language: go
framework: gin
architecture: layered
database: none
`

// fakeHome points the user's home and config directories at a temp dir and
// makes every official source unreachable, so tests never touch the real
// machine or the network. It returns the fake home.
func fakeHome(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PROJGEN_PRESETS", "")

	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // every request now fails to connect
	old := newFetcher
	newFetcher = func() *versions.Fetcher {
		return &versions.Fetcher{Client: &http.Client{Timeout: time.Second}, GoDL: srv.URL, Proxy: srv.URL,
			DockerHub: srv.URL, GitHub: srv.URL, Adoptium: srv.URL, Maven: srv.URL}
	}
	t.Cleanup(func() { newFetcher = old })
	return home
}

func TestNew_UnreachableSourcesFallBackToSavedVersions(t *testing.T) {
	home := fakeHome(t)
	var out bytes.Buffer
	if err := run([]string{"new", "--config", writeConfig(t), "--skip-tidy", "--no-git"}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "warning: some versions could not be checked") {
		t.Errorf("no warning about unreachable sources:\n%s", out.String())
	}
	gomod, err := os.ReadFile(filepath.Join(home, "source", "repos", "demo-api", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	want := versions.Gin + " " + versions.Defaults().Modules[versions.Gin]
	if !strings.Contains(string(gomod), want) {
		t.Errorf("go.mod does not use built-in %s:\n%s", want, gomod)
	}
}

func TestNew_OfflineSkipsCheck(t *testing.T) {
	fakeHome(t)
	var out bytes.Buffer
	if err := run([]string{"new", "--config", writeConfig(t), "--offline", "--dry-run"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Checking latest versions") {
		t.Errorf("--offline still checked sources:\n%s", out.String())
	}
}

func TestVersionsCommand(t *testing.T) {
	fakeHome(t)
	var out bytes.Buffer
	if err := run([]string{"versions"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Built-in versions", "Go", versions.Gin, "postgres", "actions/checkout"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("versions output missing %q:\n%s", want, out.String())
		}
	}
}

func writeConfig(t *testing.T) string {
	cfg := filepath.Join(t.TempDir(), "project.yaml")
	if err := os.WriteFile(cfg, []byte(testConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestNew_DefaultsToSourceRepos(t *testing.T) {
	home := fakeHome(t)
	var out bytes.Buffer
	if err := run([]string{"new", "--config", writeConfig(t), "--skip-tidy", "--no-git"}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}

	want := filepath.Join(home, "source", "repos", "demo-api")
	if !strings.Contains(out.String(), "Project location: "+want) {
		t.Errorf("output does not show %s:\n%s", want, out.String())
	}
	if _, err := os.Stat(filepath.Join(want, "go.mod")); err != nil {
		t.Errorf("project not created at %s: %v", want, err)
	}
}

func TestNew_InteractiveAsksLocation(t *testing.T) {
	fakeHome(t)
	parent := t.TempDir()
	// preset (custom), name, language, module, location, then defaults for the stack, no extras, confirm.
	input := "\ndemo-api\n\n\n" + parent + "\n\n\n3\n\ny\n"
	var out bytes.Buffer
	if err := run([]string{"new", "--skip-tidy", "--no-git"}, strings.NewReader(input), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	want := filepath.Join(parent, "demo-api")
	if !strings.Contains(out.String(), "Project location: "+want) {
		t.Errorf("output does not show %s:\n%s", want, out.String())
	}
	if _, err := os.Stat(filepath.Join(want, "go.mod")); err != nil {
		t.Errorf("project not created at %s: %v", want, err)
	}
}

func TestNew_OutOverridesDefault(t *testing.T) {
	fakeHome(t)
	want := filepath.Join(t.TempDir(), "custom")
	var out bytes.Buffer
	if err := run([]string{"new", "--config", writeConfig(t), "--out", want, "--dry-run"}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), want) {
		t.Errorf("dry run output does not show %s:\n%s", want, out.String())
	}
}

func TestNew_PresetAndName(t *testing.T) {
	home := fakeHome(t)
	var out bytes.Buffer
	args := []string{"new", "--preset", "techpartners-java", "--name", "pay-api", "--offline", "--no-git"}
	if err := run(args, strings.NewReader(""), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	dir := filepath.Join(home, "source", "repos", "pay-api")
	b, err := os.ReadFile(filepath.Join(dir, "project.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"language: java", "module: com.techpartners.payapi", "database: postgresql"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("project.yaml missing %q:\n%s", want, b)
		}
	}
}

func TestNew_PresetWithConfigRejected(t *testing.T) {
	fakeHome(t)
	err := run([]string{"new", "--preset", "go-minimal", "--config", writeConfig(t)}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err = %v", err)
	}
}

func TestPresetSaveAndList(t *testing.T) {
	fakeHome(t)
	var out bytes.Buffer
	if err := run([]string{"preset", "save", "my-team", "--from", writeConfig(t), "--description", "our stack"}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("save: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := run([]string{"preset", "list"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"my-team", "user", "our stack", "techpartners-go", "built-in"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("list missing %q:\n%s", want, out.String())
		}
	}
	if err := run([]string{"new", "--preset", "unknown"}, strings.NewReader(""), &out); err == nil || !strings.Contains(err.Error(), "my-team") {
		t.Errorf("unknown preset err = %v, want the available names", err)
	}
}

func TestVersion(t *testing.T) {
	fakeHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.5.0"}`))
	}))
	t.Cleanup(srv.Close)
	newFetcher = func() *versions.Fetcher { return &versions.Fetcher{Client: srv.Client(), GitHub: srv.URL} }
	oldVersion := version
	t.Cleanup(func() { version = oldVersion })

	for _, tt := range []struct {
		version string
		args    []string
		want    string
		notWant string
	}{
		{"0.4.2", nil, "projgen v0.5.0 is available", ""},
		{"0.5.0", nil, "latest release", "is available"},
		{"0.4.2", []string{"--offline"}, "projgen 0.4.2", "is available"},
	} {
		version = tt.version
		var out bytes.Buffer
		if err := run(append([]string{"version"}, tt.args...), strings.NewReader(""), &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tt.want) || (tt.notWant != "" && strings.Contains(out.String(), tt.notWant)) {
			t.Errorf("version %s %v: output %q", tt.version, tt.args, out.String())
		}
	}
}
