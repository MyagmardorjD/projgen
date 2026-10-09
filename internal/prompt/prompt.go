// Package prompt asks the developer for project options in the terminal.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/MyagmardorjD/projgen/internal/options"
)

// ErrCancelled is returned when the developer does not confirm.
var ErrCancelled = errors.New("cancelled")

// Prompter reads answers from in and writes questions to out.
type Prompter struct {
	in  *bufio.Reader
	out io.Writer
}

func New(in io.Reader, out io.Writer) *Prompter {
	return &Prompter{in: bufio.NewReader(in), out: out}
}

// Ask walks through every option, shows a summary and asks to confirm.
// It returns the options and the project directory. When fixedDir is set
// (from --out) the location question is skipped; otherwise the developer
// picks a parent folder, defaulting to defaultParent, and the project goes
// in <parent>/<name>.
func (p *Prompter) Ask(fixedDir, defaultParent string) (options.Options, string, error) {
	var o options.Options
	var err error

	if o.Name, err = p.text("Project name", "my-service"); err != nil {
		return o, "", err
	}
	if o.Module, err = p.text("Go module path", "github.com/MyagmardorjD/"+o.Name); err != nil {
		return o, "", err
	}
	dir := fixedDir
	if dir == "" {
		parent, err := p.text("Create in folder", defaultParent)
		if err != nil {
			return o, "", err
		}
		dir = filepath.Join(ExpandHome(parent), o.Name)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return o, "", err
	}
	o, err = p.askStack(o)
	if err != nil {
		return o, "", err
	}

	fmt.Fprintf(p.out, "\nSummary\n  name:         %s\n  module:       %s\n  location:     %s\n  language:     %s\n  framework:    %s\n  architecture: %s\n  database:     %s\n  extras:       %s\n",
		o.Name, o.Module, dir, o.Language, o.Framework, o.Architecture, o.Database, strings.Join(o.Extras, ", "))
	ok, err := p.text("Create project? (y/n)", "y")
	if err != nil {
		return o, "", err
	}
	if !strings.EqualFold(ok, "y") && !strings.EqualFold(ok, "yes") {
		return o, "", ErrCancelled
	}
	return o, dir, nil
}

// ExpandHome replaces a leading "~" with the user's home directory.
func ExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}

// askStack asks for the technology choices and validates them.
func (p *Prompter) askStack(o options.Options) (options.Options, error) {
	var err error
	if o.Language, err = p.choice("Language", options.Languages); err != nil {
		return o, err
	}
	if o.Framework, err = p.choice("Framework", options.Frameworks[o.Language]); err != nil {
		return o, err
	}
	if o.Architecture, err = p.choice("Architecture", options.Architectures); err != nil {
		return o, err
	}
	if o.Database, err = p.choice("Database", options.Databases); err != nil {
		return o, err
	}
	if o.Extras, err = p.multi("Extras", options.Extras); err != nil {
		return o, err
	}
	return o, o.Validate()
}

func (p *Prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		return "", fmt.Errorf("reading answer: %w", err)
	}
	return strings.TrimSpace(s), nil
}

func (p *Prompter) text(label, def string) (string, error) {
	fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	s, err := p.line()
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

// choice asks for one option by number; Enter picks the first.
func (p *Prompter) choice(label string, cs []options.Choice) (string, error) {
	for {
		fmt.Fprintf(p.out, "\n%s:\n", label)
		for i, c := range cs {
			fmt.Fprintf(p.out, "  %d) %s\n", i+1, c.Label)
		}
		fmt.Fprintf(p.out, "Choose 1-%d [1]: ", len(cs))
		s, err := p.line()
		if err != nil {
			return "", err
		}
		if s == "" {
			return cs[0].Value, nil
		}
		if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(cs) {
			return cs[n-1].Value, nil
		}
		fmt.Fprintf(p.out, "Please enter a number from 1 to %d.\n", len(cs))
	}
}

// multi asks for any number of options as "1,3"; Enter picks none.
func (p *Prompter) multi(label string, cs []options.Choice) ([]string, error) {
outer:
	for {
		fmt.Fprintf(p.out, "\n%s (comma-separated, Enter for none):\n", label)
		for i, c := range cs {
			fmt.Fprintf(p.out, "  %d) %s\n", i+1, c.Label)
		}
		fmt.Fprint(p.out, "Choose: ")
		s, err := p.line()
		if err != nil {
			return nil, err
		}
		if s == "" {
			return nil, nil
		}
		var out []string
		seen := map[int]bool{}
		for _, part := range strings.Split(s, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil || n < 1 || n > len(cs) {
				fmt.Fprintf(p.out, "%q is not a number from 1 to %d.\n", strings.TrimSpace(part), len(cs))
				continue outer
			}
			if !seen[n] {
				seen[n] = true
				out = append(out, cs[n-1].Value)
			}
		}
		return out, nil
	}
}
