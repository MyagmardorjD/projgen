package preset

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MyagmardorjD/projgen/internal/options"
)

// isolate points the user config dir and PROJGEN_PRESETS at temp folders.
func isolate(t *testing.T, teamDirs ...string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("APPDATA", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("PROJGEN_PRESETS", strings.Join(teamDirs, string(os.PathListSeparator)))
}

func write(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinsAreValid(t *testing.T) {
	isolate(t)
	all, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := map[string]bool{"techpartners-go": false, "techpartners-java": false, "go-minimal": false}
	for _, p := range all {
		if p.Source != SourceBuiltin {
			t.Errorf("%s source = %s", p.Name, p.Source)
		}
		if _, ok := want[p.Name]; ok {
			want[p.Name] = true
		}
		if err := p.Options("my-service").Validate(); err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("built-in preset %s missing", n)
		}
	}
}

func TestOptionsModule(t *testing.T) {
	isolate(t)
	java, _ := Find("techpartners-java")
	if got := java.Options("order-service").Module; got != "com.techpartners.orderservice" {
		t.Errorf("java module = %s", got)
	}
	p := Preset{Language: "go", ModulePrefix: "gitlab.techpartners.asia/backend"}
	if got := p.Options("billing").Module; got != "gitlab.techpartners.asia/backend/billing" {
		t.Errorf("go module = %s", got)
	}
}

func TestPrecedenceAndBadFiles(t *testing.T) {
	team := t.TempDir()
	isolate(t, team)
	user, _ := UserDir()

	write(t, team, "techpartners-go.yaml", "name: techpartners-go\ndescription: team version\nlanguage: go\nframework: echo\narchitecture: layered\ndatabase: mysql\n")
	write(t, team, "payments.yml", "name: payments\nlanguage: go\nframework: fiber\narchitecture: hexagonal\ndatabase: postgresql\nextras: [docker]\n")
	write(t, team, "broken.yaml", "name: broken\nlanguage: cobol\n")
	write(t, user, "payments.yaml", "name: payments\ndescription: my version\nlanguage: go\nframework: gin\narchitecture: clean\ndatabase: none\n")

	all, err := Load()
	if err == nil || !strings.Contains(err.Error(), "broken.yaml") {
		t.Errorf("err = %v, want broken.yaml reported", err)
	}
	got := map[string]Preset{}
	for _, p := range all {
		got[p.Name] = p
	}
	if p := got["techpartners-go"]; p.Source != SourceTeam || p.Framework != "echo" {
		t.Errorf("team should replace built-in: %+v", p)
	}
	if p := got["payments"]; p.Source != SourceUser || p.Framework != "gin" {
		t.Errorf("user should replace team: %+v", p)
	}
	if _, ok := got["broken"]; ok {
		t.Error("invalid preset loaded")
	}
	if _, ok := got["go-minimal"]; !ok {
		t.Error("built-in go-minimal lost")
	}
}

func TestFindUnknown(t *testing.T) {
	isolate(t)
	if _, err := Find("nope"); err == nil || !strings.Contains(err.Error(), "techpartners-go") {
		t.Fatalf("err = %v, want available names", err)
	}
}

func TestSaveAndReload(t *testing.T) {
	isolate(t)
	o := options.Options{Name: "billing", Module: "gitlab.techpartners.asia/backend/billing", Language: "go",
		Framework: "echo", Architecture: "hexagonal", Database: "mysql", Extras: []string{"docker"}}
	p := FromOptions("backend-go", "Backend team Go stack", o)
	if p.ModulePrefix != "gitlab.techpartners.asia/backend" {
		t.Errorf("prefix = %s", p.ModulePrefix)
	}

	path, err := Save(p, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Save(p, false); err == nil {
		t.Error("second save without force should fail")
	}
	if _, err := Save(p, true); err != nil {
		t.Errorf("force save: %v", err)
	}

	got, err := Find("backend-go")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceUser || got.Path != path || got.Options("pay").Module != "gitlab.techpartners.asia/backend/pay" {
		t.Errorf("reloaded = %+v", got)
	}

	if _, err := Save(Preset{Name: "Bad Name", Language: "go"}, false); err == nil {
		t.Error("invalid preset saved")
	}
}

func TestFromOptionsJava(t *testing.T) {
	o := options.Options{Name: "x", Module: "com.techpartners.payments.x", Language: "java", Framework: "spring-boot",
		Architecture: "clean", Database: "none"}
	if p := FromOptions("pay", "", o); p.ModulePrefix != "com.techpartners.payments" {
		t.Errorf("prefix = %s", p.ModulePrefix)
	}
}
