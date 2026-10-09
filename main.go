// Command projgen creates a new project from the technologies a developer picks.
//
//	projgen new                       ask questions interactively
//	projgen new --config project.yaml read choices from a file
//	projgen list                      show supported options
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/techpartners/projgen/internal/generator"
	"github.com/techpartners/projgen/internal/options"
	"github.com/techpartners/projgen/internal/prompt"
)

const version = "0.1.0"

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return errors.New("missing command")
	}
	switch args[0] {
	case "new":
		return cmdNew(args[1:], in, out)
	case "list":
		cmdList(out)
		return nil
	case "version":
		fmt.Fprintln(out, "projgen", version)
		return nil
	case "help", "-h", "--help":
		usage(out)
		return nil
	}
	usage(out)
	return fmt.Errorf("unknown command %q", args[0])
}

func usage(out io.Writer) {
	fmt.Fprint(out, `projgen - create a new project from the technologies you choose

Usage:
  projgen new [flags]   create a project (asks questions unless --config is given)
  projgen list          show supported languages, frameworks, architectures, databases, extras
  projgen version

Flags for new:
  --config FILE   read choices from a YAML file instead of asking
  --out DIR       target directory (default ./<name>)
  --dry-run       list files without writing them
  --force         write into a non-empty directory
  --skip-tidy     do not run "go mod tidy" after generating
  --no-git        do not run "git init"
`)
}

func cmdNew(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("new", flag.ContinueOnError)
	fs.SetOutput(out)
	cfgPath := fs.String("config", "", "")
	outDir := fs.String("out", "", "")
	dryRun := fs.Bool("dry-run", false, "")
	force := fs.Bool("force", false, "")
	skipTidy := fs.Bool("skip-tidy", false, "")
	noGit := fs.Bool("no-git", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var o options.Options
	if *cfgPath != "" {
		b, err := os.ReadFile(*cfgPath)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(b, &o); err != nil {
			return fmt.Errorf("%s: %w", *cfgPath, err)
		}
		if err := o.Validate(); err != nil {
			return fmt.Errorf("%s:\n%w", *cfgPath, err)
		}
	} else {
		var err error
		if o, err = prompt.New(in, out).Ask(); err != nil {
			return err
		}
	}

	dir := *outDir
	if dir == "" {
		dir = o.Name
	}
	res, err := generator.Generate(o, dir, generator.Flags{DryRun: *dryRun, Force: *force})
	if err != nil {
		return err
	}

	if *dryRun {
		fmt.Fprintf(out, "Dry run: would create %d files in %s\n", len(res.Files), dir)
		for _, f := range res.Files {
			fmt.Fprintln(out, "  "+f)
		}
		return nil
	}
	fmt.Fprintf(out, "Created %d files in %s\n", len(res.Files), dir)

	if !*skipTidy {
		fmt.Fprintln(out, "Running go mod tidy (downloads dependencies)...")
		if err := runIn(dir, out, "go", "mod", "tidy"); err != nil {
			fmt.Fprintf(out, "warning: go mod tidy failed: %v\nRun it yourself once you are online.\n", err)
		}
	}
	if !*noGit {
		if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
			if err := runIn(dir, out, "git", "init", "-q"); err != nil {
				fmt.Fprintf(out, "warning: git init failed: %v\n", err)
			}
		}
	}

	fmt.Fprintf(out, "\nNext steps:\n  cd %s\n  go test ./...\n  go run ./cmd/server\n", dir)
	return nil
}

func runIn(dir string, out io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func cmdList(out io.Writer) {
	section := func(title string, cs []options.Choice) {
		fmt.Fprintf(out, "%s:\n", title)
		for _, c := range cs {
			fmt.Fprintf(out, "  %-16s %s\n", c.Value, c.Label)
		}
	}
	section("Languages", options.Languages)
	for _, l := range options.Languages {
		section("Frameworks ("+l.Value+")", options.Frameworks[l.Value])
	}
	section("Architectures", options.Architectures)
	section("Databases", options.Databases)
	section("Extras", options.Extras)
}
