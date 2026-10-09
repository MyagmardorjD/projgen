// Command projgen creates a new project from the technologies a developer picks.
//
//	projgen new                       ask questions interactively
//	projgen new --config project.yaml read choices from a file
//	projgen list                      show supported options
//	projgen update                    fetch latest technology versions
//	projgen versions                  show the versions new projects will use
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/prompt"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

// refreshAfter is how old cached versions may get before "new" refreshes them.
const refreshAfter = 24 * time.Hour

// newFetcher is replaced in tests.
var newFetcher = versions.NewFetcher

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
	case "update":
		return cmdUpdate(out)
	case "versions":
		cmdVersions(out)
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
  projgen update        fetch the latest versions from official sources
  projgen versions      show the versions new projects will use
  projgen version

Flags for new:
  --config FILE   read choices from a YAML file instead of asking
  --out DIR       project directory; skips the location question
                  (default ~/source/repos/<name>)
  --dry-run       list files without writing them
  --force         write into a non-empty directory
  --skip-tidy     do not run "go mod tidy" after generating
  --no-git        do not run "git init"
  --offline       do not check official sources for newer versions
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
	offline := fs.Bool("offline", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var o options.Options
	dir := *outDir
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
		if dir == "" {
			dir = filepath.Join(defaultParent(), o.Name)
		}
	} else {
		var err error
		if o, dir, err = prompt.New(in, out).Ask(dir, defaultParent()); err != nil {
			return err
		}
	}

	// Show the full path so the developer knows exactly where the project is.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	v := currentVersions(out, *offline)
	res, err := generator.Generate(o, v, dir, generator.Flags{DryRun: *dryRun, Force: *force})
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

	fmt.Fprintf(out, "\nProject location: %s\n", dir)
	fmt.Fprintf(out, "\nNext steps:\n  cd \"%s\"\n  go test ./...\n  go run ./cmd/server\n", dir)
	return nil
}

// currentVersions returns the versions to generate with. Unless offline, it
// refreshes them from the official sources when the cache is missing or older
// than refreshAfter. Failures only warn: cached or built-in versions are used.
func currentVersions(out io.Writer, offline bool) versions.Versions {
	v, cached := versions.Load()
	if offline || (cached && time.Since(v.FetchedAt) < refreshAfter) {
		return v
	}
	fmt.Fprintln(out, "Checking latest versions from official sources...")
	updated, err := refresh(v)
	if err != nil {
		fmt.Fprintf(out, "warning: some versions could not be checked, using saved ones:\n%v\n", err)
	}
	return updated
}

// refresh fetches, merges over old and saves. It returns the merged versions
// and any fetch error.
func refresh(old versions.Versions) (versions.Versions, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	got, fetchErr := newFetcher().Fetch(ctx)
	v := versions.Merge(old, got)
	if err := versions.Save(v); err != nil {
		fetchErr = errors.Join(fetchErr, fmt.Errorf("saving cache: %w", err))
	}
	return v, fetchErr
}

func cmdUpdate(out io.Writer) error {
	old, _ := versions.Load()
	fmt.Fprintln(out, "Checking latest versions from official sources...")
	v, err := refresh(old)
	printVersions(out, old, v)
	if err != nil {
		return fmt.Errorf("some versions could not be checked (saved ones kept):\n%w", err)
	}
	path, _ := versions.CachePath()
	fmt.Fprintf(out, "\nSaved to %s\n", path)
	return nil
}

func cmdVersions(out io.Writer) {
	v, cached := versions.Load()
	if cached {
		fmt.Fprintf(out, "Versions checked %s (run \"projgen update\" to refresh):\n", v.FetchedAt.Local().Format("2006-01-02 15:04"))
	} else {
		fmt.Fprintln(out, "Built-in versions (run \"projgen update\" to check official sources):")
	}
	printVersions(out, v, v)
}

// printVersions lists every tracked version, marking ones that changed.
func printVersions(out io.Writer, old, cur versions.Versions) {
	row := func(name, source, before, after string) {
		mark := ""
		if before != after {
			mark = "  (was " + before + ")"
		}
		fmt.Fprintf(out, "  %-32s %-12s %-18s%s\n", name, after, source, mark)
	}
	row("Go", "go.dev", old.Go, cur.Go)
	for _, m := range versions.Modules {
		row(m, "proxy.golang.org", old.Modules[m], cur.Modules[m])
	}
	row("postgres (docker image)", "Docker Hub", old.Images["postgres"], cur.Images["postgres"])
	row("mysql LTS (docker image)", "Docker Hub", old.Images["mysql"], cur.Images["mysql"])
	for _, a := range versions.Actions {
		row(a, "GitHub", old.Actions[a], cur.Actions[a])
	}
}

// defaultParent is where projects go unless the developer chooses otherwise:
// the user's local repositories folder, ~/source/repos.
func defaultParent() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "source", "repos")
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
