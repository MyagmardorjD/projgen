package entity

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

func TestParse_Refs(t *testing.T) {
	tests := []struct {
		name    string
		entity  string
		fields  []string
		target  string
		wantErr string
	}{
		{"named after the entity", "order_item", []string{"product:ref"}, "product", ""},
		{"explicit entity", "Post", []string{"author:ref:Person"}, "Person", ""},
		{"required changes nothing", "order_item", []string{"product:ref:required"}, "product", ""},
		{"_id suffix", "order_item", []string{"product_id:ref"}, "", "without _id"},
		{"itself", "Category", []string{"parent:ref:Category"}, "", "itself"},
		{"bad entity", "Post", []string{"author:ref:1x"}, "", "not an entity name"},
		{"column clash", "order_item", []string{"product:ref", "product_id:int"}, "", "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Parse(tt.entity, tt.fields)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if f := s.Fields[0]; f.Ref != tt.target || f.Column() != f.Name+"_id" {
				t.Errorf("ref = %q, column = %q", f.Ref, f.Column())
			}
		})
	}
}

func TestAdd_RefNeedsTheEntity(t *testing.T) {
	for _, dir := range []string{newProject(t, combos()[1]), newProject(t, javaCombos()[1])} {
		spec, _ := Parse("order_item", itemFields)
		if _, err := Add(dir, spec, false, time.Now()); err == nil || !strings.Contains(err.Error(), "add it first") {
			t.Errorf("err = %v, want a hint to add Product first", err)
		}
	}
}

// A project whose entities were added before refs existed gets its shared
// files and the referred repository patched to answer 409.
func TestAdd_RefPatchesOlderFiles(t *testing.T) {
	o := combos()[0] // gin, layered, postgresql
	dir := newProject(t, o)
	mustAdd(t, dir, "Product", testFields)
	d := generator.NewData(o, versions.Defaults())
	paths := map[string]string{
		"errors":  filepath.Join(dir, d.P["domain"].Dir, "errors.go"),
		"helpers": filepath.Join(dir, d.P["http"].Dir, "entity_helpers.go"),
		"repo":    filepath.Join(dir, d.P["db"].Dir, "product_repository.go"),
	}
	// Undo what the current templates add, leaving the files as older projgen wrote them.
	dom := d.P["domain"].Name
	strip := map[string][]string{
		"errors":  {"\t// ErrConflict is returned when other records still refer to a record.\n\tErrConflict = errors.New(\"conflict\")\n"},
		"helpers": {"\tcase errors.Is(err, " + dom + ".ErrConflict):\n\t\tc.JSON(http.StatusConflict, gin.H{\"error\": err.Error()})\n"},
		"repo":    {"\t\tif isForeignKeyViolation(err) {\n\t\t\treturn fmt.Errorf(\"%w: other records still refer to this product\", " + dom + ".ErrConflict)\n\t\t}\n"},
	}
	for k, cuts := range strip {
		b, err := os.ReadFile(paths[k])
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, c := range cuts {
			if !strings.Contains(s, c) {
				t.Fatalf("%s does not contain %q", k, c)
			}
			s = strings.Replace(s, c, "", 1)
		}
		os.WriteFile(paths[k], []byte(s), 0o644)
	}
	os.Remove(filepath.Join(dir, d.P["db"].Dir, "sql_errors.go"))

	res := mustAdd(t, dir, "order_item", itemFields)
	if len(res.Manual) != 0 {
		t.Errorf("manual = %v", res.Manual)
	}
	if len(res.Modified) != 5 {
		t.Errorf("modified = %v, want router, main, errors, helpers and the product repository", res.Modified)
	}
	for k, cuts := range strip {
		b, _ := os.ReadFile(paths[k])
		if _, err := parser.ParseFile(token.NewFileSet(), paths[k], b, 0); err != nil {
			t.Errorf("%s no longer parses: %v", k, err)
		}
		if !strings.Contains(string(b), strings.TrimSpace(cuts[0][:strings.Index(cuts[0], "\n")])) {
			t.Errorf("%s was not patched:\n%s", k, b)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, d.P["db"].Dir, "sql_errors.go")); err != nil {
		t.Error("sql_errors.go not created")
	}

	// Java: the shared exception handler gets the 409 mapping.
	jo := javaCombos()[1]
	jdir := newProject(t, jo)
	mustAdd(t, jdir, "Product", testFields)
	jd := generator.NewData(jo, versions.Defaults())
	handler := filepath.Join(jdir, jd.P["http"].Dir, "EntityExceptionHandler.java")
	b, _ := os.ReadFile(handler)
	old := strings.Replace(string(b), javaConflictHandler, "", 1)
	old = strings.Replace(old, "import org.springframework.dao.DataIntegrityViolationException;\n", "", 1)
	os.WriteFile(handler, []byte(old), 0o644)
	jres := mustAdd(t, jdir, "order_item", itemFields)
	if len(jres.Modified) != 1 {
		t.Errorf("java modified = %v, want the exception handler", jres.Modified)
	}
	if b, _ := os.ReadFile(handler); !strings.Contains(string(b), "HttpStatus.CONFLICT") || !strings.Contains(string(b), "dao.DataIntegrityViolationException") {
		t.Errorf("handler not patched:\n%s", b)
	}
}
