// Package create writes a project to disk and runs the follow-up steps
// (go mod tidy, git init). It is shared by the CLI and the web UI.
package create

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

// Steps choose which follow-up commands run after the files are written.
type Steps struct {
	Tidy bool // go mod tidy
	Git  bool // git init
}

// Warning is a follow-up step that failed; the project itself was created.
type Warning struct {
	Step string
	Err  error
}

// DefaultParent is where projects go unless the developer chooses otherwise:
// the user's local repositories folder, ~/source/repos.
func DefaultParent() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "source", "repos")
}

// Project generates o into dir, then runs the chosen steps, writing their
// output to out. Step failures are returned as warnings, not errors.
func Project(o options.Options, v versions.Versions, dir string, fl generator.Flags, st Steps, out io.Writer) (generator.Result, []Warning, error) {
	res, err := generator.Generate(o, v, dir, fl)
	if err != nil || fl.DryRun {
		return res, nil, err
	}
	var warns []Warning
	if st.Tidy {
		if err := runIn(dir, out, "go", "mod", "tidy"); err != nil {
			warns = append(warns, Warning{"go mod tidy", err})
		}
	}
	if st.Git {
		if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
			if err := runIn(dir, out, "git", "init", "-q", "-b", "main"); err != nil {
				warns = append(warns, Warning{"git init", err})
			}
		}
	}
	return res, warns, nil
}

func runIn(dir string, out io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
