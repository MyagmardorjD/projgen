package entity

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
	"time"

	"golang.org/x/tools/go/ast/astutil"
	"golang.org/x/tools/imports"
	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

//go:embed templates
var templateFS embed.FS

// ErrExists is returned when a file for the entity already exists and Force is off.
var ErrExists = errors.New("entity files already exist")

// Names holds the spellings of an entity name.
type Names struct {
	Name   string // OrderItem
	Var    string // orderItem
	Snake  string // order_item
	Table  string // order_items
	Route  string // order-items
	Plural string // OrderItems (field name in Deps)
}

// FieldData is a Field with every spelling templates need.
type FieldData struct {
	Field
	Go         string // UnitPrice
	GoType     string // float64
	JSON       string // unit_price
	Column     string // unit_price
	SQLType    string // DOUBLE PRECISION
	Sample     string // Go literal used in tests
	SampleJSON string // JSON literal used in tests
}

// SQL holds pre-built SQL fragments for the repository template.
type SQL struct {
	SelectCols, InsertCols, InsertVals, UpdateSet, UpdateWhere string
	ScanArgs, ExecArgs, P1, P2                                 string
}

// Data is passed to every entity template.
type Data struct {
	generator.Data
	E             Names
	Fields        []FieldData
	FirstRequired *FieldData
	SampleJSON    string // "name":"sample","price":9.5
	SQL           SQL
	MigrationsRel string // migrations folder relative to the repository package
}

// Result lists what Add did.
type Result struct {
	Created  []string // new files, relative to the project
	Modified []string // existing files that were wired up
	Skipped  []string // shared files that already existed
	Manual   []string // wiring the developer must add by hand (no markers found)

	AutoMigrate bool // the project applies migrations on start
}

type outFile struct {
	path   string // slash-separated, relative to the project
	tmpl   string
	shared bool // generated once and reused by later entities
}

// Add generates the entity into the project at dir, which must contain the
// project.yaml written by projgen.
func Add(dir string, spec Spec, force bool, now time.Time) (Result, error) {
	var res Result
	o, err := readProject(dir)
	if err != nil {
		return res, err
	}
	d := newData(o, spec)
	files := plan(d, now)

	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f.path))); err == nil && !f.shared && !force {
			return res, fmt.Errorf("%w: %s (use --force to overwrite)", ErrExists, f.path)
		}
	}

	// Render everything before writing anything, so a failure leaves the project untouched.
	rendered := map[string][]byte{}
	for _, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(f.path))
		if _, err := os.Stat(full); err == nil && f.shared {
			res.Skipped = append(res.Skipped, f.path)
			continue
		}
		b, err := render(f.tmpl, d)
		if err != nil {
			return res, err
		}
		if strings.HasSuffix(f.path, ".go") {
			if b, err = imports.Process(full, b, &imports.Options{Comments: true, TabIndent: true, TabWidth: 8}); err != nil {
				return res, fmt.Errorf("template %s produced invalid Go: %w", f.tmpl, err)
			}
		}
		rendered[f.path] = b
	}
	edits, manual, err := wiring(dir, d)
	if err != nil {
		return res, err
	}

	for _, f := range files {
		b, ok := rendered[f.path]
		if !ok {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(f.path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			return res, err
		}
		res.Created = append(res.Created, f.path)
	}
	for _, e := range edits {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(e.path)), e.content, 0o644); err != nil {
			return res, err
		}
		res.Modified = append(res.Modified, e.path)
	}
	res.Manual = manual
	res.AutoMigrate = o.HasExtra("migrations")
	return res, nil
}

func readProject(dir string) (options.Options, error) {
	var o options.Options
	b, err := os.ReadFile(filepath.Join(dir, "project.yaml"))
	if err != nil {
		return o, fmt.Errorf("%s is not a projgen project (no project.yaml): %w", dir, err)
	}
	if err := yaml.Unmarshal(b, &o); err != nil {
		return o, fmt.Errorf("project.yaml: %w", err)
	}
	if err := o.Validate(); err != nil {
		return o, fmt.Errorf("project.yaml: %w", err)
	}
	if o.Language != "go" {
		return o, fmt.Errorf("add entity supports Go projects only for now; this is a %s project", o.Language)
	}
	return o, nil
}

func newData(o options.Options, s Spec) Data {
	d := Data{Data: generator.NewData(o, versions.Defaults())}
	sn := snake(s.Name)
	table := plural(sn)
	d.E = Names{
		Name:   pascal(s.Name),
		Var:    camel(s.Name),
		Snake:  sn,
		Table:  table,
		Route:  strings.ReplaceAll(table, "_", "-"),
		Plural: pascal(table),
	}
	pg := o.Database == "postgresql"
	ph := func(i int) string {
		if pg {
			return fmt.Sprintf("$%d", i)
		}
		return "?"
	}
	var sel, ins, vals, set, scan, args, sample []string
	sel = append(sel, "id")
	scan = append(scan, "&x.ID")
	for i, f := range s.Fields {
		fd := FieldData{Field: f, Go: pascal(f.Name), JSON: f.Name, Column: f.Name}
		fd.GoType, fd.SQLType, fd.Sample, fd.SampleJSON = typeInfo(f.Type, pg)
		d.Fields = append(d.Fields, fd)
		sel = append(sel, f.Name)
		ins = append(ins, f.Name)
		vals = append(vals, ph(i+1))
		set = append(set, f.Name+" = "+ph(i+1))
		scan = append(scan, "&x."+fd.Go)
		args = append(args, "in."+fd.Go)
		sample = append(sample, fmt.Sprintf("%q:%s", f.Name, fd.SampleJSON))
	}
	for i := range d.Fields {
		if d.Fields[i].Required {
			d.FirstRequired = &d.Fields[i]
			break
		}
	}
	sel = append(sel, "created_at", "updated_at")
	scan = append(scan, "&x.CreatedAt", "&x.UpdatedAt")
	d.SampleJSON = strings.Join(sample, ",")
	d.MigrationsRel = strings.Repeat("../", strings.Count(d.P["db"].Dir, "/")+1) + "migrations"
	d.SQL = SQL{
		SelectCols:  strings.Join(sel, ", "),
		InsertCols:  strings.Join(ins, ", "),
		InsertVals:  strings.Join(vals, ", "),
		UpdateSet:   strings.Join(set, ", "),
		UpdateWhere: ph(len(s.Fields) + 1),
		ScanArgs:    strings.Join(scan, ", "),
		ExecArgs:    strings.Join(args, ", "),
		P1:          ph(1),
		P2:          ph(2),
	}
	return d
}

// typeInfo returns Go type, SQL type, Go sample and JSON sample for a field type.
func typeInfo(t string, pg bool) (goType, sqlType, sample, sampleJSON string) {
	pick := func(p, m string) string {
		if pg {
			return p
		}
		return m
	}
	switch t {
	case "string":
		return "string", "VARCHAR(255)", `"sample"`, `"sample"`
	case "text":
		return "string", "TEXT", `"sample text"`, `"sample text"`
	case "int":
		return "int", pick("INTEGER", "INT"), "1", "1"
	case "int64":
		return "int64", "BIGINT", "1", "1"
	case "float":
		return "float64", pick("DOUBLE PRECISION", "DOUBLE"), "9.5", "9.5"
	case "bool":
		return "bool", "BOOLEAN", "true", "true"
	case "time":
		return "time.Time", pick("TIMESTAMPTZ", "DATETIME(6)"), "time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC)", `"2026-01-02T15:04:05Z"`
	}
	panic("entity: unknown type " + t)
}

func plan(d Data, now time.Time) []outFile {
	p := d.P
	fs := []outFile{
		{p["domain"].Dir + "/errors.go", "errors.go.tmpl", true},
		{p["domain"].Dir + "/" + d.E.Snake + ".go", "domain.go.tmpl", false},
		{p["service"].Dir + "/" + d.E.Snake + "_service.go", "service.go.tmpl", false},
		{p["service"].Dir + "/" + d.E.Snake + "_service_test.go", "service_test.go.tmpl", false},
		{p["http"].Dir + "/entity_helpers.go", "http/helpers.go.tmpl", true},
		{p["http"].Dir + "/" + d.E.Snake + "_handler.go", "http/handler.go.tmpl", false},
		{p["http"].Dir + "/" + d.E.Snake + "_handler_test.go", "http/handler_test.go.tmpl", false},
	}
	if d.HasDB {
		stamp := now.UTC().Format("20060102150405")
		fs = append(fs,
			outFile{p["db"].Dir + "/" + d.E.Snake + "_repository.go", "repo_sql.go.tmpl", false},
			outFile{p["db"].Dir + "/" + d.E.Snake + "_repository_test.go", "repo_sql_test.go.tmpl", false},
			outFile{"migrations/" + stamp + "_create_" + d.E.Table + ".up.sql", "migration.up.sql.tmpl", false},
			outFile{"migrations/" + stamp + "_create_" + d.E.Table + ".down.sql", "migration.down.sql.tmpl", false},
		)
	} else {
		fs = append(fs, outFile{p["db"].Dir + "/" + d.E.Snake + "_repository.go", "repo_memory.go.tmpl", false})
	}
	return fs
}

func render(name string, d Data) ([]byte, error) {
	t, err := template.New("").Option("missingkey=error").ParseFS(templateFS, "templates/"+name, "templates/fake_repo.tmpl")
	if err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, filepath.Base(name), d); err != nil {
		return nil, fmt.Errorf("template %s: %w", name, err)
	}
	return buf.Bytes(), nil
}

type edit struct {
	path    string
	content []byte
}

const (
	markerDeps   = "// projgen:deps"
	markerRoutes = "// projgen:routes"
	markerWire   = "// projgen:wire"
)

// wiring adds the service to Deps, the routes to the router and the
// construction to main.go. Files without markers are reported as manual steps.
func wiring(dir string, d Data) ([]edit, []string, error) {
	routerPath := d.P["http"].Dir + "/router.go"
	mainPath := "cmd/server/main.go"
	svc := d.P["service"].Name
	repo := d.P["db"].Name

	depsLine := fmt.Sprintf("%s *%s.%sService", d.E.Plural, svc, d.E.Name)
	var routeLine string
	if d.Opt.Framework == "nethttp" {
		routeLine = fmt.Sprintf("register%sRoutes(mux, \"/api/v1\", d.%s, d.Logger)", d.E.Name, d.E.Plural)
	} else {
		routeLine = fmt.Sprintf("register%sRoutes(v1, d.%s, d.Logger)", d.E.Name, d.E.Plural)
	}
	repoArg := "conn"
	if !d.HasDB {
		repoArg = ""
	}
	wireLine := fmt.Sprintf("deps.%s = %s.New%sService(%s.New%sRepository(%s))", d.E.Plural, svc, d.E.Name, repo, d.E.Name, repoArg)

	var edits []edit
	var manual []string

	router, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(routerPath)))
	if err != nil {
		return nil, nil, err
	}
	wired := regexp.MustCompile(`(?m)^\s*` + d.E.Plural + `\s+\*`)
	if wired.Match(router) {
		// Already wired (re-run with --force).
	} else if bytes.Contains(router, []byte(markerDeps)) && bytes.Contains(router, []byte(markerRoutes)) {
		out := insertBefore(router, markerDeps, depsLine)
		out = insertBefore(out, markerRoutes, routeLine)
		formatted, err := format.Source(out)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", routerPath, err)
		}
		edits = append(edits, edit{routerPath, formatted})
	} else {
		manual = append(manual,
			fmt.Sprintf("%s: add to the Deps struct:\n    %s", routerPath, depsLine),
			fmt.Sprintf("%s: in NewRouter, after the /hello route:\n    %s", routerPath, routeLine))
	}

	mainSrc, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(mainPath)))
	if err != nil {
		return nil, nil, err
	}
	if bytes.Contains(mainSrc, []byte(wireLine)) {
		// Already wired.
	} else if bytes.Contains(mainSrc, []byte(markerWire)) {
		out := insertBefore(mainSrc, markerWire, wireLine)
		out, err = addImport(out, repo, d.P["db"].Import)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", mainPath, err)
		}
		edits = append(edits, edit{mainPath, out})
	} else {
		manual = append(manual, fmt.Sprintf("%s: before the server starts (import %s %q):\n    %s", mainPath, repo, d.P["db"].Import, wireLine))
	}
	return edits, manual, nil
}

// insertBefore inserts line above the line holding marker, with its indentation.
func insertBefore(src []byte, marker, line string) []byte {
	lines := strings.Split(string(src), "\n")
	for i, l := range lines {
		if strings.Contains(l, marker) {
			indent := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			lines = append(lines[:i], append([]string{indent + line}, lines[i:]...)...)
			break
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func addImport(src []byte, name, path string) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	astutil.AddNamedImport(fset, f, name, path)
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
