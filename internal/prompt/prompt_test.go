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
			input:     "order-service\n\n" + other + "\n1\n9\n2\n2\n2\n1,2,2\ny\n",
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
			o, dir, err := New(strings.NewReader(tt.input), io.Discard).Ask("", parent)
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

func TestAsk_FixedDirSkipsLocationQuestion(t *testing.T) {
	fixed := filepath.Join(t.TempDir(), "here")
	var out strings.Builder
	// No answer for the location: the next line goes to Language.
	_, dir, err := New(strings.NewReader("\n\n\n\n\n\n\n\n"), &out).Ask(fixed, "unused")
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
	_, _, err := New(strings.NewReader("\n\n\n\n\n\n\n2\n"), io.Discard).Ask("", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "requires docker") {
		t.Fatalf("err = %v, want compose/docker error", err)
	}
}

func TestAsk_EndOfInput(t *testing.T) {
	if _, _, err := New(strings.NewReader(""), io.Discard).Ask("", t.TempDir()); err == nil {
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
