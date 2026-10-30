package entity

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// maxIdentifier is the longest constraint or index name PostgreSQL keeps (MySQL allows 64).
const maxIdentifier = 63

// resolveRefs checks that every entity a ref field points to exists, plans
// the rows the repository test seeds, and returns the edits that make
// existing files report a blocked delete as a conflict.
func resolveRefs(dir string, d *Data) ([]edit, []string, error) {
	if len(d.Refs) == 0 {
		return nil, nil, nil
	}
	for i, r := range d.Refs {
		file := r.Target.Snake + ".go"
		if d.Java {
			file = r.Target.Name + ".java"
		}
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(d.P["domain"].Dir), file)); err != nil {
			return nil, nil, fmt.Errorf("field %q refers to %s, which this project does not have; add it first: projgen add entity %s ...",
				r.Name, r.Target.Name, r.Target.Name)
		}
		if len(r.FK) > maxIdentifier || len(r.Index) > maxIdentifier {
			return nil, nil, fmt.Errorf("field %q: constraint name %s is longer than %d characters; use a shorter entity or field name",
				r.Name, r.FK, maxIdentifier)
		}
		d.Refs[i].MapsConflict = d.HasDB && !d.Java
	}
	if !d.HasDB {
		return nil, nil, nil
	}

	var tables []string
	for _, r := range d.Refs {
		tables = append(tables, r.Target.Table)
	}
	seeds, err := planSeeds(dir, d.Java, d.Opt.Database == "postgresql", tables)
	if err != nil {
		return nil, nil, err
	}
	d.Seeds = seeds

	edits, manual, err := conflictEdits(dir, *d)
	if err != nil {
		return nil, nil, err
	}
	// A parent whose Delete could not be patched still refuses the delete
	// (the foreign key does), but answers 500 instead of 409.
	for i, r := range d.Refs {
		for _, m := range manual {
			if strings.HasPrefix(m, d.P["db"].Dir+"/"+r.Target.Snake+"_repository.go:") {
				d.Refs[i].MapsConflict = false
			}
		}
	}
	for i := range d.Fields {
		for _, r := range d.Refs {
			if d.Fields[i].Name == r.Name {
				d.Fields[i] = r
			}
		}
	}
	return edits, manual, nil
}

func seedVar(table string) string { return camel(table) + "ID" }

type column struct {
	name, sqlType string
	ref           string // table a foreign key points to
}

var (
	columnRe = regexp.MustCompile(`^\s+([a-z][a-z0-9_]*) ([A-Z][A-Z0-9(), ]*?) NOT NULL,?$`)
	fkRe     = regexp.MustCompile(`FOREIGN KEY \(([a-z0-9_]+)\) REFERENCES ([a-z0-9_]+) \(id\)`)
)

// readTable reads the columns a row of table needs from the migration
// projgen wrote for it.
func readTable(dir string, java bool, table string) ([]column, error) {
	pattern := filepath.Join(dir, "migrations", "*_create_"+table+".up.sql")
	if java {
		pattern = filepath.Join(dir, "src", "main", "resources", "db", "migration", "V*__create_"+table+".sql")
	}
	files, _ := filepath.Glob(pattern)
	if len(files) == 0 {
		return nil, fmt.Errorf("no migration creates the %s table (looked for %s)", table, filepath.Base(pattern))
	}
	slices.Sort(files)
	b, err := os.ReadFile(files[len(files)-1])
	if err != nil {
		return nil, err
	}
	var cols []column
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if m := columnRe.FindStringSubmatch(l); m != nil && m[1] != "created_at" && m[1] != "updated_at" {
			cols = append(cols, column{name: m[1], sqlType: m[2]})
		}
		if m := fkRe.FindStringSubmatch(l); m != nil {
			for i := range cols {
				if cols[i].name == m[1] {
					cols[i].ref = m[2]
				}
			}
		}
	}
	return cols, nil
}

// literal is a value of an SQL column type for a test row.
func literal(sqlType string) (string, bool) {
	t := strings.ToUpper(sqlType)
	switch {
	case strings.HasPrefix(t, "VARCHAR"), strings.HasPrefix(t, "CHAR"), t == "TEXT":
		return "'sample'", true
	case strings.HasPrefix(t, "INT"), t == "BIGINT", t == "SMALLINT":
		return "1", true
	case strings.HasPrefix(t, "DOUBLE"), t == "REAL", strings.HasPrefix(t, "NUMERIC"), strings.HasPrefix(t, "DECIMAL"), t == "FLOAT":
		return "9.5", true
	case t == "BOOLEAN", t == "BOOL":
		return "TRUE", true
	case strings.HasPrefix(t, "TIMESTAMP"), strings.HasPrefix(t, "DATETIME"), t == "DATE":
		return "'2026-01-02 15:04:05'", true
	}
	return "", false
}

// planSeeds returns one row per table the tables refer to, directly or
// through their own refs, parents first.
func planSeeds(dir string, java, pg bool, tables []string) ([]Seed, error) {
	var out []Seed
	done := map[string]bool{}
	var visit func(table string, path []string) error
	visit = func(table string, path []string) error {
		if done[table] {
			return nil
		}
		if slices.Contains(path, table) {
			return fmt.Errorf("tables refer to each other in a cycle: %s", strings.Join(append(path, table), " -> "))
		}
		cols, err := readTable(dir, java, table)
		if err != nil {
			return err
		}
		s := Seed{Table: table, Var: seedVar(table)}
		var names, goVals, javaVals []string
		for _, c := range cols {
			names = append(names, c.name)
			if c.ref != "" {
				if err := visit(c.ref, append(path, table)); err != nil {
					return err
				}
				s.Args = append(s.Args, seedVar(c.ref))
				ph := "?"
				if pg {
					ph = fmt.Sprintf("$%d", len(s.Args))
				}
				goVals, javaVals = append(goVals, ph), append(javaVals, "?")
				continue
			}
			v, ok := literal(c.sqlType)
			if !ok {
				return fmt.Errorf("%s.%s: cannot make a test value for type %s", table, c.name, c.sqlType)
			}
			goVals, javaVals = append(goVals, v), append(javaVals, v)
		}
		insert := func(vals []string) string {
			if len(names) == 0 {
				if pg {
					return "INSERT INTO " + table + " DEFAULT VALUES"
				}
				return "INSERT INTO " + table + " () VALUES ()"
			}
			return "INSERT INTO " + table + " (" + strings.Join(names, ", ") + ") VALUES (" + strings.Join(vals, ", ") + ")"
		}
		s.GoSQL, s.JavaSQL = insert(goVals), insert(javaVals)
		done[table] = true
		out = append(out, s)
		return nil
	}
	for _, t := range tables {
		if err := visit(t, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// conflictEdits makes files generated before refs existed answer 409 when a
// delete is blocked by a foreign key. Files that already do are left alone;
// files that changed too much to patch are reported as manual steps.
func conflictEdits(dir string, d Data) ([]edit, []string, error) {
	var edits []edit
	var manual []string
	read := func(rel string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		return b, err == nil
	}

	if d.Java {
		rel := d.P["http"].Dir + "/EntityExceptionHandler.java"
		src, ok := read(rel)
		if !ok || bytes.Contains(src, []byte("DataIntegrityViolationException")) {
			return nil, nil, nil // new projects get it from the template
		}
		out, ok := patchJavaHandler(src)
		if !ok {
			return nil, []string{rel + ": map org.springframework.dao.DataIntegrityViolationException to 409 Conflict"}, nil
		}
		return []edit{{rel, out}}, nil, nil
	}

	dom := d.P["domain"].Name
	errorsRel := d.P["domain"].Dir + "/errors.go"
	if src, ok := read(errorsRel); ok && !bytes.Contains(src, []byte("ErrConflict")) {
		out := insertBeforeLast(src, ")", "\t// ErrConflict is returned when other records still refer to a record.\n\tErrConflict = errors.New(\"conflict\")")
		if f, err := format.Source(out); err == nil {
			edits = append(edits, edit{errorsRel, f})
		} else {
			manual = append(manual, errorsRel+`: add ErrConflict = errors.New("conflict")`)
		}
	}

	helpersRel := d.P["http"].Dir + "/entity_helpers.go"
	if src, ok := read(helpersRel); ok && !bytes.Contains(src, []byte("ErrConflict")) {
		out, ok := patchHelpers(src, dom)
		if f, err := format.Source(out); ok && err == nil {
			edits = append(edits, edit{helpersRel, f})
		} else {
			manual = append(manual, helpersRel+": in entityError, answer 409 Conflict for "+dom+".ErrConflict")
		}
	}

	for _, r := range d.Refs {
		rel := d.P["db"].Dir + "/" + r.Target.Snake + "_repository.go"
		src, ok := read(rel)
		if !ok || bytes.Contains(src, []byte("isForeignKeyViolation")) || slices.ContainsFunc(edits, func(e edit) bool { return e.path == rel }) {
			continue
		}
		out, ok := patchDelete(src, r.Target, dom)
		f, err := format.Source(out)
		if !ok || err != nil {
			manual = append(manual, fmt.Sprintf("%s: in Delete, return %s.ErrConflict when isForeignKeyViolation(err)", rel, dom))
			continue
		}
		edits = append(edits, edit{rel, f})
	}
	return edits, manual, nil
}

// insertBeforeLast inserts text above the last line that is exactly marker.
func insertBeforeLast(src []byte, marker, text string) []byte {
	lines := strings.Split(string(src), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == marker {
			lines = slices.Insert(lines, i, text)
			break
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// patchHelpers adds an ErrConflict case after the ErrValidation case of entityError.
func patchHelpers(src []byte, dom string) ([]byte, bool) {
	lines := strings.Split(string(src), "\n")
	for i, l := range lines {
		if strings.Contains(l, "case errors.Is(err, "+dom+".ErrValidation):") && i+1 < len(lines) &&
			strings.Contains(lines[i+1], "http.StatusUnprocessableEntity") {
			body := strings.ReplaceAll(lines[i+1], "http.StatusUnprocessableEntity", "http.StatusConflict")
			add := []string{strings.Replace(l, "ErrValidation", "ErrConflict", 1), body}
			return []byte(strings.Join(slices.Insert(lines, i+2, add...), "\n")), true
		}
	}
	return src, false
}

// patchDelete makes a repository's Delete return ErrConflict when a foreign key blocks it.
func patchDelete(src []byte, e Names, dom string) ([]byte, bool) {
	s := string(src)
	at := strings.Index(s, `"DELETE FROM `+e.Table+` WHERE id = `)
	if at < 0 {
		return src, false
	}
	ret := `return fmt.Errorf("delete ` + e.Snake + `: %w", err)`
	i := strings.Index(s[at:], ret)
	if i < 0 {
		return src, false
	}
	i += at
	check := "if isForeignKeyViolation(err) {\n\t\t\treturn fmt.Errorf(\"%w: other records still refer to this " + e.Snake + "\", " + dom + ".ErrConflict)\n\t\t}\n\t\t"
	return []byte(s[:i] + check + s[i:]), true
}

const javaConflictHandler = `
    @ExceptionHandler(DataIntegrityViolationException.class)
    public ResponseEntity<Map<String, String>> conflict(DataIntegrityViolationException e) {
        return ResponseEntity.status(HttpStatus.CONFLICT).body(Map.of("error", "conflict with related records"));
    }`

// patchJavaHandler adds the 409 handler to an EntityExceptionHandler generated before refs.
func patchJavaHandler(src []byte) ([]byte, bool) {
	s := string(src)
	imp := "import org.springframework.http.HttpStatus;"
	if !strings.Contains(s, imp) {
		return src, false
	}
	s = strings.Replace(s, imp, "import org.springframework.dao.DataIntegrityViolationException;\n"+imp, 1)
	return insertBeforeLast([]byte(s), "}", javaConflictHandler), true
}
