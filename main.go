// Command projgen creates a new project from the technologies a developer picks.
//
//	projgen new                       ask questions interactively
//	projgen new --config project.yaml read choices from a file
//	projgen list                      show supported options
//	projgen add entity Product name:string:required price:float
//	projgen serve                     pick options in the browser
//	projgen update                    fetch latest technology versions
//	projgen versions                  show the versions new projects will use
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/create"
	"github.com/MyagmardorjD/projgen/internal/entity"
	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/preset"
	"github.com/MyagmardorjD/projgen/internal/prompt"
	"github.com/MyagmardorjD/projgen/internal/versions"
	"github.com/MyagmardorjD/projgen/internal/web"
)

// refreshAfter is how old cached versions may get before "new" refreshes them.
const refreshAfter = 24 * time.Hour

// newFetcher is replaced in tests.
var newFetcher = versions.NewFetcher

// version and commit are set by GoReleaser (-X main.version=... -X main.commit=...).
// A "go install ...@vX.Y.Z" build reads its version from the build info instead.
var (
	version = ""
	commit  = ""
)

// buildVersion returns projgen's version without a leading v, or "dev".
func buildVersion() string {
	if version != "" {
		return strings.TrimPrefix(version, "v")
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

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
	case "add":
		return cmdAdd(args[1:], out)
	case "preset":
		return cmdPreset(args[1:], out)
	case "serve":
		return cmdServe(args[1:], out)
	case "update":
		return cmdUpdate(out)
	case "versions":
		cmdVersions(out)
		return nil
	case "version", "--version":
		return cmdVersion(args[1:], out)
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
  projgen add entity NAME FIELD...
                        add a CRUD resource to the project in the current folder
                        FIELD is name:type or name:type:required
  projgen preset list   show presets (built-in, team folders in PROJGEN_PRESETS, your own)
  projgen preset save NAME [--from project.yaml] [--description TEXT] [--force]
                        save a project's choices as your preset
  projgen serve         open the web UI to pick options, download a ZIP or create locally
  projgen list          show supported languages, frameworks, architectures, databases, extras
  projgen update        fetch the latest versions from official sources
  projgen versions      show the versions new projects will use
  projgen version [--offline]
                        show the version and whether a newer release exists

Flags for new:
  --preset NAME   start from a preset; asks only name, module and location
  --name NAME     with --preset: ask nothing at all
  --config FILE   read choices from a YAML file instead of asking
  --out DIR       project directory; skips the location question
                  (default ~/source/repos/<name>)
  --dry-run       list files without writing them
  --force         write into a non-empty directory
  --skip-tidy     do not run "go mod tidy" after generating
  --no-git        do not run "git init"
  --offline       do not check official sources for newer versions

Flags for add entity:
  --dir DIR       project folder (default: current folder)
  --force         overwrite files the entity already has

Flags for serve:
  --port N        port on 127.0.0.1 (default 8090)
  --no-browser    do not open the browser
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
	presetName := fs.String("preset", "", "")
	projectName := fs.String("name", "", "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cfgPath != "" && *presetName != "" {
		return errors.New("use either --config or --preset, not both")
	}

	var o options.Options
	dir := *outDir
	var chosen *preset.Preset
	if *presetName != "" {
		p, err := preset.Find(*presetName)
		if err != nil {
			return err
		}
		chosen = &p
	}

	if chosen != nil && *projectName != "" {
		// Fully non-interactive: preset + name.
		o = chosen.Options(*projectName)
		if err := o.Validate(); err != nil {
			return err
		}
		if dir == "" {
			dir = filepath.Join(create.DefaultParent(), o.Name)
		}
	} else if *cfgPath != "" {
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
			dir = filepath.Join(create.DefaultParent(), o.Name)
		}
	} else {
		req := prompt.Request{FixedDir: dir, DefaultParent: create.DefaultParent(), Preset: chosen}
		if chosen == nil {
			presets, loadErr := preset.Load()
			if loadErr != nil {
				fmt.Fprintf(out, "warning: some presets could not be loaded:\n%v\n", loadErr)
			}
			req.Presets = presets
		}
		var err error
		if o, dir, err = prompt.New(in, out).Ask(req); err != nil {
			return err
		}
	}

	// Show the full path so the developer knows exactly where the project is.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	v := currentVersions(out, *offline)
	if !*dryRun && !*skipTidy && o.Language == "go" {
		fmt.Fprintln(out, "Writing files, then running go mod tidy (downloads dependencies)...")
	}
	steps := create.Steps{Tidy: !*skipTidy, Git: !*noGit}
	res, warns, err := create.Project(o, v, dir, generator.Flags{DryRun: *dryRun, Force: *force}, steps, out)
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
	for _, w := range warns {
		fmt.Fprintf(out, "warning: %s failed: %v\n", w.Step, w.Err)
	}

	fmt.Fprintf(out, "\nProject location: %s\n", dir)
	if o.Language == "java" {
		fmt.Fprintf(out, "\nNext steps (JDK %s):\n  cd \"%s\"\n  ./mvnw verify            (Windows: mvnw.cmd verify)\n  ./mvnw spring-boot:run\n",
			v.Java[versions.JavaLTS], dir)
		return nil
	}
	fmt.Fprintf(out, "\nNext steps:\n  cd \"%s\"\n  go test ./...\n  go run ./cmd/server\n", dir)
	return nil
}

func cmdPreset(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: projgen preset list | projgen preset save NAME [--from project.yaml] [--description TEXT] [--force]")
	}
	switch args[0] {
	case "list":
		presets, err := preset.Load()
		for _, p := range presets {
			fmt.Fprintf(out, "  %-22s %-9s %s\n", p.Name, p.Source, p.Summary())
			if p.Description != "" {
				fmt.Fprintf(out, "  %-22s %-9s %s\n", "", "", p.Description)
			}
		}
		if dir, e := preset.UserDir(); e == nil {
			fmt.Fprintf(out, "\nYour presets: %s\nTeam folders: PROJGEN_PRESETS=%s\n", dir, strings.Join(preset.TeamDirs(), string(os.PathListSeparator)))
		}
		if err != nil {
			fmt.Fprintf(out, "\nwarning: some presets could not be loaded:\n%v\n", err)
		}
		return nil
	case "save":
		fs := flag.NewFlagSet("preset save", flag.ContinueOnError)
		fs.SetOutput(out)
		from := fs.String("from", "project.yaml", "")
		description := fs.String("description", "", "")
		force := fs.Bool("force", false, "")
		// Accept the name before or after the flags.
		var name string
		rest := args[1:]
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			name, rest = rest[0], rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if name == "" && fs.NArg() > 0 {
			name = fs.Arg(0)
		}
		if name == "" {
			return errors.New("missing preset name")
		}
		b, err := os.ReadFile(*from)
		if err != nil {
			return err
		}
		var o options.Options
		if err := yaml.Unmarshal(b, &o); err != nil {
			return fmt.Errorf("%s: %w", *from, err)
		}
		p := preset.FromOptions(name, *description, o)
		path, err := preset.Save(p, *force)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Saved preset %s (%s) to %s\nShare it with your team by putting the file in a folder listed in PROJGEN_PRESETS.\n", p.Name, p.Summary(), path)
		return nil
	}
	return fmt.Errorf("unknown preset command %q (use list or save)", args[0])
}

func cmdAdd(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "entity" {
		return errors.New(`usage: projgen add entity NAME FIELD... (e.g. projgen add entity Product name:string:required price:float)`)
	}
	dir, force := ".", false
	var rest []string
	for i := 1; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--force":
			force = true
		case a == "--dir" && i+1 < len(args):
			dir = args[i+1]
			i++
		case strings.HasPrefix(a, "--dir="):
			dir = strings.TrimPrefix(a, "--dir=")
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s", a)
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		return errors.New("missing entity name")
	}
	spec, err := entity.Parse(rest[0], rest[1:])
	if err != nil {
		return fmt.Errorf("%w\nfield types: %s", err, entity.TypeNames)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	res, err := entity.Add(abs, spec, force, time.Now())
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "Added %s to %s\n", rest[0], abs)
	for _, f := range res.Created {
		fmt.Fprintln(out, "  created  "+f)
	}
	for _, f := range res.Modified {
		fmt.Fprintln(out, "  updated  "+f)
	}
	for _, f := range res.Skipped {
		fmt.Fprintln(out, "  kept     "+f)
	}
	if len(res.Manual) > 0 {
		fmt.Fprintln(out, "\nThis project has no projgen markers; add these lines by hand:")
		for _, m := range res.Manual {
			fmt.Fprintln(out, "  "+m)
		}
	}
	fmt.Fprintln(out, "\nNext steps:")
	for _, f := range res.Created {
		if !strings.HasSuffix(f, ".up.sql") && !strings.HasPrefix(f, "src/main/resources/db/migration/") {
			continue
		}
		if res.AutoMigrate {
			fmt.Fprintf(out, "  %s is applied on the next start (MIGRATE_ON_START=true)\n", f)
		} else {
			fmt.Fprintf(out, "  apply the migration: %s\n", f)
		}
	}
	if res.Java {
		fmt.Fprintln(out, "  ./mvnw verify            (Windows: mvnw.cmd verify)")
		return nil
	}
	fmt.Fprintln(out, "  go test ./...")
	return nil
}

func cmdServe(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(out)
	port := fs.Int("port", 8090, "")
	noBrowser := fs.Bool("no-browser", false, "")
	offline := fs.Bool("offline", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Listen on loopback only: the UI can write to this computer's disk.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		return fmt.Errorf("cannot listen on port %d (try --port): %w", *port, err)
	}
	addr := ln.Addr().(*net.TCPAddr)

	s := web.New(currentVersions(out, *offline), refresh)
	s.AllowHost(fmt.Sprintf("127.0.0.1:%d", addr.Port))
	s.AllowHost(fmt.Sprintf("localhost:%d", addr.Port))
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}

	url := fmt.Sprintf("http://127.0.0.1:%d/", addr.Port)
	fmt.Fprintf(out, "projgen UI: %s  (Ctrl+C to stop)\n", url)
	if !*noBrowser {
		if err := web.OpenBrowser(url); err != nil {
			fmt.Fprintf(out, "Open %s in your browser.\n", url)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
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

func cmdVersion(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(out)
	offline := fs.Bool("offline", false, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	line := "projgen " + buildVersion()
	if commit != "" {
		line += " (" + commit + ")"
	}
	fmt.Fprintln(out, line)
	if !*offline {
		checkSelf(out)
	}
	return nil
}

// checkSelf tells the developer when a newer projgen release exists. It is
// a hint only, so a failed check prints nothing.
func checkSelf(out io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	latest, err := newFetcher().LatestProjgen(ctx)
	if err != nil {
		return
	}
	cur := "v" + buildVersion()
	if semver.IsValid(cur) && semver.Compare(cur, latest) >= 0 {
		fmt.Fprintln(out, "This is the latest release.")
		return
	}
	fmt.Fprintf(out, "projgen %s is available. Update with:\n  go install github.com/%s@latest\nor download it from https://github.com/%s/releases/latest\n",
		latest, versions.ProjgenRepo, versions.ProjgenRepo)
}

func cmdUpdate(out io.Writer) error {
	old, _ := versions.Load()
	fmt.Fprintln(out, "Checking latest versions from official sources...")
	v, err := refresh(old)
	printVersions(out, old, v)
	checkSelf(out)
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
	row("redis (docker image)", "Docker Hub", old.Images["redis"], cur.Images["redis"])
	for _, a := range versions.Actions {
		row(a, "GitHub", old.Actions[a], cur.Actions[a])
	}
	row("Java LTS", "api.adoptium.net", old.Java[versions.JavaLTS], cur.Java[versions.JavaLTS])
	for _, k := range versions.JavaKeys[1:] {
		row(k, "Maven Central", old.Java[k], cur.Java[k])
	}
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
