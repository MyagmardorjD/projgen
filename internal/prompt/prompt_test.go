package prompt

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/MyagmardorjD/projgen/internal/preset"
)

func TestAsk(t *testing.T) {
	parent := t.TempDir()
	other := t.TempDir()

	tests := []struct {
		name      string
		input     string
		wantErr   error
		wantDir   string
		wantFW    string
		wantArch  string
		wantDB    string
		wantExtra []string
	}{
		{
			name:     "all defaults",
			input:    "\n\n\n\n\n\n\n\n\n",
			wantDir:  filepath.Join(parent, "my-service"),
			wantFW:   "gin",
			wantArch: "layered",
			wantDB:   "postgresql",
		},
		{
			name:      "explicit choices with retry on bad input",
			input:     "order-service\n1\n\n" + other + "\n9\n2\n2\n2\n1,2,2\ny\n",
			wantDir:   filepath.Join(other, "order-service"),
			wantFW:    "echo",
			wantArch:  "clean",
			wantDB:    "mysql",
			wantExtra: []string{"docker", "docker-compose"},
		},
		{
			name:    "declined",
			input:   "\n\n\n\n\n\n\n\nn\n",
			wantErr: ErrCancelled,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, dir, err := New(strings.NewReader(tt.input), io.Discard).Ask(Request{DefaultParent: parent})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if dir != tt.wantDir {
				t.Errorf("dir = %s, want %s", dir, tt.wantDir)
			}
			if o.Framework != tt.wantFW || o.Architecture != tt.wantArch || o.Database != tt.wantDB {
				t.Errorf("got %s/%s/%s, want %s/%s/%s", o.Framework, o.Architecture, o.Database, tt.wantFW, tt.wantArch, tt.wantDB)
			}
			if !slices.Equal(o.Extras, tt.wantExtra) {
				t.Errorf("extras = %v, want %v", o.Extras, tt.wantExtra)
			}
		})
	}
}

func TestAsk_Java(t *testing.T) {
	parent := t.TempDir()
	// name, language 2 (Java), default package, location, defaults, no extras, confirm
	o, dir, err := New(strings.NewReader("order-service\n2\n\n"+parent+"\n\n\n\n\n\n"), io.Discard).Ask(Request{DefaultParent: parent})
	if err != nil {
		t.Fatal(err)
	}
	if o.Language != "java" || o.Framework != "spring-boot" || o.Module != "com.techpartners.orderservice" {
		t.Errorf("got %s/%s/%s", o.Language, o.Framework, o.Module)
	}
	if dir != filepath.Join(parent, "order-service") {
		t.Errorf("dir = %s", dir)
	}
}

var testPresets = []preset.Preset{
	{Name: "team-go", Language: "go", Framework: "echo", Architecture: "hexagonal", Database: "mysql",
		Extras: []string{"docker"}, ModulePrefix: "gitlab.techpartners.asia/backend", Source: preset.SourceTeam},
	{Name: "team-java", Language: "java", Framework: "spring-boot", Architecture: "clean", Database: "none",
		Source: preset.SourceBuiltin},
}

func TestAsk_Presets(t *testing.T) {
	parent := t.TempDir()
	tests := []struct {
		name       string
		input      string
		wantFW     string
		wantModule string
		wantExtras int
	}{
		// preset 2 (team-go), name, default module, default location, confirm: no stack questions
		{"team preset", "2\nbilling\n\n\n\n", "echo", "gitlab.techpartners.asia/backend/billing", 1},
		{"java preset", "3\npay\n\n\n\n", "spring-boot", "com.techpartners.pay", 0},
		// preset 1 = custom: the usual questions follow
		{"custom", "1\nshop\n\n\n\n\n\n\n\n\n", "gin", "github.com/MyagmardorjD/shop", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o, _, err := New(strings.NewReader(tt.input), io.Discard).Ask(Request{DefaultParent: parent, Presets: testPresets})
			if err != nil {
				t.Fatal(err)
			}
			if o.Framework != tt.wantFW || o.Module != tt.wantModule || len(o.Extras) != tt.wantExtras {
				t.Errorf("got %s %s %v", o.Framework, o.Module, o.Extras)
			}
		})
	}
}

func TestAsk_FixedPresetSkipsPresetQuestion(t *testing.T) {
	var out strings.Builder
	// name, default module, default location, confirm
	o, _, err := New(strings.NewReader("billing\n\n\n\n"), &out).Ask(Request{DefaultParent: t.TempDir(), Preset: &testPresets[0]})
	if err != nil {
		t.Fatal(err)
	}
	if o.Framework != "echo" || strings.Contains(out.String(), "Start from a preset") {
		t.Errorf("framework = %s; output:\n%s", o.Framework, out.String())
	}
	if !strings.Contains(out.String(), "Preset: team-go (team)") {
		t.Errorf("summary does not name the preset:\n%s", out.String())
	}
}

func TestAsk_FixedDirSkipsLocationQuestion(t *testing.T) {
	fixed := filepath.Join(t.TempDir(), "here")
	var out strings.Builder
	// No answer for the location: the next line goes to Language.
	_, dir, err := New(strings.NewReader("\n\n\n\n\n\n\n\n"), &out).Ask(Request{FixedDir: fixed, DefaultParent: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	if dir != fixed {
		t.Errorf("dir = %s, want %s", dir, fixed)
	}
	if strings.Contains(out.String(), "Create in folder") {
		t.Error("location was asked although --out was given")
	}
}

func TestAsk_InvalidCombination(t *testing.T) {
	// docker-compose (2) without docker (1)
	_, _, err := New(strings.NewReader("\n\n\n\n\n\n\n2\n"), io.Discard).Ask(Request{DefaultParent: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "requires docker") {
		t.Fatalf("err = %v, want compose/docker error", err)
	}
}

func TestAsk_EndOfInput(t *testing.T) {
	if _, _, err := New(strings.NewReader(""), io.Discard).Ask(Request{DefaultParent: t.TempDir()}); err == nil {
		t.Fatal("expected an error when input ends")
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	tests := map[string]string{
		"~":                 home,
		"~/source/repos":    filepath.Join(home, "source", "repos"),
		"D:/Work":           "D:/Work",
		"relative/~/folder": "relative/~/folder",
	}
	if runtime.GOOS == "windows" {
		tests[`~\source\repos`] = filepath.Join(home, "source", "repos")
	}
	for in, want := range tests {
		if got := ExpandHome(in); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}
