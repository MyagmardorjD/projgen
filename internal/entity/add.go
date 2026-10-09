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
	"slices"
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

	Java       string // unitPrice (record component)
	JavaType   string // double (entity record)
	JavaBoxed  string // Double (input record, null when absent)
	JavaZero   string // value an absent input gets; empty when the field must be sent
	JavaSample string // Java literal used in tests
	JavaGet    string // ResultSet getter: getDouble
	JavaParam  string // JDBC parameter for the input value: in.unitPrice()
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
	HasTime       bool   // some field is a time
}

// Result lists what Add did.
type Result struct {
	Created  []string // new files, relative to the project
	Modified []string // existing files that were wired up
	Skipped  []string // shared files that already existed
	Manual   []string // wiring the developer must add by hand (no markers found)

	AutoMigrate bool // the project applies migrations on start
	Java        bool // the project is a Java (Spring Boot) project
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
	if o.Language == "java" {
		if err := checkJava(spec); err != nil {
			return res, err
		}
	}
	d := newData(o, spec)
	files := plan(d, migrationStamp(dir, d.Java, now))

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
	// Spring finds Java components by scanning, so only Go needs wiring.
	var edits []edit
	var manual []string
	if !d.Java {
		if edits, manual, err = wiring(dir, d); err != nil {
			return res, err
		}
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
	res.Java = d.Java
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
	java := o.Language == "java"
	ph := func(i int) string {
		if pg && !java { // JDBC always uses ?
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
		fd.Java = camel(f.Name)
		fd.JavaType, fd.JavaBoxed, fd.JavaZero, fd.JavaSample, fd.JavaGet = javaTypeInfo(f.Type)
		fd.JavaParam = "in." + fd.Java + "()"
		if f.Type == "time" {
			fd.JavaParam = "Timestamp.from(" + fd.JavaParam + ")"
			d.HasTime = true
		}
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

// javaTypeInfo returns the entity type, input type, zero value, test sample
// and ResultSet getter of a field type.
func javaTypeInfo(t string) (typ, boxed, zero, sample, get string) {
	switch t {
	case "string":
		return "String", "String", `""`, `"sample"`, "getString"
	case "text":
		return "String", "String", `""`, `"sample text"`, "getString"
	case "int":
		return "int", "Integer", "0", "1", "getInt"
	case "int64":
		return "long", "Long", "0L", "1L", "getLong"
	case "float":
		return "double", "Double", "0.0", "9.5", "getDouble"
	case "bool":
		return "boolean", "Boolean", "false", "true", "getBoolean"
	case "time":
		// There is no sensible zero time, so a time must always be sent.
		return "Instant", "Instant", "", `Instant.parse("2026-01-02T15:04:05Z")`, "getTimestamp"
	}
	panic("entity: unknown type " + t)
}

// Names a Java entity or field cannot take: keywords, and classes the
// generated code uses without a package.
var (
	javaKeywords = []string{"abstract", "assert", "boolean", "break", "byte", "case", "catch", "char", "class", "const",
		"continue", "default", "do", "double", "else", "enum", "extends", "final", "finally", "float", "for", "goto",
		"if", "implements", "import", "instanceof", "int", "interface", "long", "native", "new", "package", "private",
		"protected", "public", "return", "short", "static", "strictfp", "super", "switch", "synchronized", "this",
		"throw", "throws", "transient", "try", "void", "volatile", "while", "true", "false", "null", "var", "record",
		"yield", "sealed", "permits", "when",
		// Methods of java.lang.Object; a record component with these names would clash.
		"hashCode", "toString", "getClass", "notify", "notifyAll", "wait", "clone", "finalize", "equals"}
	javaClasses = []string{"String", "Object", "Integer", "Long", "Double", "Boolean", "Instant", "Timestamp", "List",
		"Map", "ArrayList", "Override", "Record", "Exception", "RuntimeException", "Class", "System", "Math", "Void",
		"Repository", "Service", "Test", "Optional", "Entity", "Validation", "NotFound"}
)

// checkJava rejects names that are valid for Go but not for Java.
func checkJava(s Spec) error {
	var errs []error
	if slices.Contains(javaClasses, pascal(s.Name)) {
		errs = append(errs, fmt.Errorf("entity name %q clashes with a Java class the generated code uses; choose another", s.Name))
	}
	for _, f := range s.Fields {
		if slices.Contains(javaKeywords, camel(f.Name)) {
			errs = append(errs, fmt.Errorf("field %q is reserved in Java; choose another", f.Name))
		}
	}
	return errors.Join(errs...)
}

func plan(d Data, stamp string) []outFile {
	if d.Java {
		return javaPlan(d, stamp)
	}
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

func javaPlan(d Data, stamp string) []outFile {
	p, n := d.P, d.E.Name
	fs := []outFile{
		{p["domain"].Dir + "/NotFoundException.java", "java/NotFoundException.java.tmpl", true},
		{p["domain"].Dir + "/ValidationException.java", "java/ValidationException.java.tmpl", true},
		{p["domain"].Dir + "/" + n + ".java", "java/Entity.java.tmpl", false},
		{p["domain"].Dir + "/" + n + "Input.java", "java/Input.java.tmpl", false},
		{p["domain"].Dir + "/" + n + "Repository.java", "java/Repository.java.tmpl", false},
		{p["service"].Dir + "/" + n + "Service.java", "java/Service.java.tmpl", false},
		{p["service"].TestDir + "/" + n + "ServiceTest.java", "java/ServiceTest.java.tmpl", false},
		{p["http"].Dir + "/EntityExceptionHandler.java", "java/EntityExceptionHandler.java.tmpl", true},
		{p["http"].Dir + "/" + n + "Controller.java", "java/Controller.java.tmpl", false},
		{p["http"].TestDir + "/" + n + "ControllerTest.java", "java/ControllerTest.java.tmpl", false},
	}
	if d.HasDB {
		fs = append(fs,
			outFile{p["db"].Dir + "/Jdbc" + n + "Repository.java", "java/JdbcRepository.java.tmpl", false},
			outFile{p["db"].TestDir + "/Jdbc" + n + "RepositoryTest.java", "java/JdbcRepositoryTest.java.tmpl", false},
			// Tests use an in-memory repository; in main it would clash with the JDBC bean.
			outFile{p["db"].TestDir + "/InMemory" + n + "Repository.java", "java/InMemoryRepository.java.tmpl", false},
			outFile{"src/main/resources/db/migration/V" + stamp + "__create_" + d.E.Table + ".sql", "migration.up.sql.tmpl", false},
		)
	} else {
		fs = append(fs, outFile{p["db"].Dir + "/InMemory" + n + "Repository.java", "java/InMemoryRepository.java.tmpl", false})
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

const stampLayout = "20060102150405"

var (
	goMigrationRe   = regexp.MustCompile(`^(\d{14})_`)
	javaMigrationRe = regexp.MustCompile(`^V(\d{14})__`)
)

// migrationStamp returns the version for a new migration: now, or one second
// after the newest existing migration when that is not older, so entities
// added within the same second never share a version.
func migrationStamp(dir string, java bool, now time.Time) string {
	folder, re := "migrations", goMigrationRe
	if java {
		folder, re = filepath.Join("src", "main", "resources", "db", "migration"), javaMigrationRe
	}
	next := now.UTC().Truncate(time.Second)
	entries, _ := os.ReadDir(filepath.Join(dir, folder))
	for _, e := range entries {
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if t, err := time.Parse(stampLayout, m[1]); err == nil && !t.Before(next) {
			next = t.Add(time.Second)
		}
	}
	return next.Format(stampLayout)
}
