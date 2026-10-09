package entity

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

func TestNames(t *testing.T) {
	tests := []struct{ in, pascal, camel, snake, plural string }{
		{"Product", "Product", "product", "product", "products"},
		{"order_item", "OrderItem", "orderItem", "order_item", "order_items"},
		{"OrderItem", "OrderItem", "orderItem", "order_item", "order_items"},
		{"category", "Category", "category", "category", "categories"},
		{"Address", "Address", "address", "address", "addresses"},
		{"Box", "Box", "box", "box", "boxes"},
		{"key_day", "KeyDay", "keyDay", "key_day", "key_days"},
		{"HTTPLog", "HTTPLog", "httpLog", "http_log", "http_logs"},
		{"category_id", "CategoryID", "categoryID", "category_id", "category_ids"},
	}
	for _, tt := range tests {
		if got := pascal(tt.in); got != tt.pascal {
			t.Errorf("pascal(%q) = %q, want %q", tt.in, got, tt.pascal)
		}
		if got := camel(tt.in); got != tt.camel {
			t.Errorf("camel(%q) = %q, want %q", tt.in, got, tt.camel)
		}
		if got := snake(tt.in); got != tt.snake {
			t.Errorf("snake(%q) = %q, want %q", tt.in, got, tt.snake)
		}
		if got := plural(snake(tt.in)); got != tt.plural {
			t.Errorf("plural(%q) = %q, want %q", tt.in, got, tt.plural)
		}
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		entity  string
		fields  []string
		wantErr string
	}{
		{"valid", "Product", []string{"name:string:required", "price:float", "released_at:time"}, ""},
		{"no fields", "Product", nil, "at least one field"},
		{"bad entity name", "1Product", []string{"name:string"}, "entity name"},
		{"sql keyword entity", "Order", []string{"name:string"}, ""},
		{"sql keyword table", "group", []string{"name:string"}, "SQL keyword"},
		{"bad field format", "Product", []string{"name"}, "name:type"},
		{"unknown type", "Product", []string{"name:varchar"}, "not supported"},
		{"camelCase field", "Product", []string{"unitPrice:float"}, "snake_case"},
		{"automatic field", "Product", []string{"id:int64"}, "added automatically"},
		{"keyword field", "Product", []string{"order:int"}, "SQL keyword"},
		{"required on number", "Product", []string{"price:float:required"}, "string and text"},
		{"bad modifier", "Product", []string{"name:string:unique"}, "only modifier"},
		{"duplicate field", "Product", []string{"name:string", "name:text"}, "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.entity, tt.fields)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

var testFields = []string{"name:string:required", "description:text", "price:float", "stock:int",
	"weight:int64", "active:bool", "released_at:time"}

func combos() []options.Options {
	var out []options.Options
	for _, fw := range options.Frameworks["go"] {
		for _, a := range options.Architectures {
			for _, d := range options.Databases {
				out = append(out, options.Options{
					Name: "shop", Module: "example.com/shop", Language: "go",
					Framework: fw.Value, Architecture: a.Value, Database: d.Value,
				})
			}
		}
	}
	return out
}

func newProject(t *testing.T, o options.Options) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := generator.Generate(o, versions.Defaults(), dir, generator.Flags{}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustAdd(t *testing.T, dir, name string, fields []string) Result {
	t.Helper()
	spec, err := Parse(name, fields)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Add(dir, spec, false, time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("add %s: %v", name, err)
	}
	return res
}

func TestAdd_AllCombinations(t *testing.T) {
	for _, o := range combos() {
		t.Run(o.Framework+"-"+o.Architecture+"-"+o.Database, func(t *testing.T) {
			dir := newProject(t, o)
			res := mustAdd(t, dir, "Product", testFields)
			if len(res.Manual) != 0 {
				t.Errorf("manual steps for a fresh project: %v", res.Manual)
			}
			if len(res.Modified) != 2 {
				t.Errorf("modified = %v, want router.go and main.go", res.Modified)
			}
			wantFiles := 8
			if o.Database != "none" {
				wantFiles = 11
			}
			if len(res.Created) != wantFiles {
				t.Errorf("created %d files, want %d: %v", len(res.Created), wantFiles, res.Created)
			}

			// A second entity reuses the shared files and wires up next to the first.
			res2 := mustAdd(t, dir, "order_item", []string{"quantity:int", "note:string"})
			if len(res2.Skipped) != 2 {
				t.Errorf("second entity skipped = %v, want errors.go and entity_helpers.go", res2.Skipped)
			}
			d := generator.NewData(o, versions.Defaults())
			router, _ := os.ReadFile(filepath.Join(dir, d.P["http"].Dir, "router.go"))
			for _, want := range []string{`Products\s+\*`, `OrderItems\s+\*`, `registerProductRoutes\(`, `registerOrderItemRoutes\(`} {
				if !regexp.MustCompile(want).Match(router) {
					t.Errorf("router.go missing %s", want)
				}
			}
			main, _ := os.ReadFile(filepath.Join(dir, "cmd", "server", "main.go"))
			for _, want := range []string{"deps.Products = ", "deps.OrderItems = ", d.P["db"].Import} {
				if !strings.Contains(string(main), want) {
					t.Errorf("main.go missing %q", want)
				}
			}
		})
	}
}

func TestAdd_ExistingEntityNeedsForce(t *testing.T) {
	o := combos()[0]
	dir := newProject(t, o)
	mustAdd(t, dir, "Product", testFields)

	spec, _ := Parse("Product", testFields)
	if _, err := Add(dir, spec, false, time.Now()); !errors.Is(err, ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	res, err := Add(dir, spec, true, time.Now())
	if err != nil {
		t.Fatalf("force: %v", err)
	}
	if len(res.Modified) != 0 {
		t.Errorf("force re-wired an already wired entity: %v", res.Modified)
	}
}

func TestAdd_OldProjectWithoutMarkers(t *testing.T) {
	o := combos()[0]
	dir := newProject(t, o)
	d := generator.NewData(o, versions.Defaults())
	for _, f := range []string{filepath.Join(d.P["http"].Dir, "router.go"), filepath.Join("cmd", "server", "main.go")} {
		p := filepath.Join(dir, f)
		b, _ := os.ReadFile(p)
		var kept []string
		for _, l := range strings.Split(string(b), "\n") {
			if !strings.Contains(l, "// projgen:") {
				kept = append(kept, l)
			}
		}
		os.WriteFile(p, []byte(strings.Join(kept, "\n")), 0o644)
	}

	res := mustAdd(t, dir, "Product", testFields)
	if len(res.Manual) != 3 || len(res.Modified) != 0 {
		t.Fatalf("manual = %v, modified = %v; want 3 manual steps and no edits", res.Manual, res.Modified)
	}
	if !strings.Contains(strings.Join(res.Manual, "\n"), "registerProductRoutes(") {
		t.Errorf("manual steps do not show the route line: %v", res.Manual)
	}
}

func TestAdd_NotAProject(t *testing.T) {
	spec, _ := Parse("Product", testFields)
	if _, err := Add(t.TempDir(), spec, false, time.Now()); err == nil || !strings.Contains(err.Error(), "project.yaml") {
		t.Fatalf("err = %v, want a project.yaml error", err)
	}
}

func goRun(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestAdded_BuildAndTest builds and tests every combination with two
// entities added. Needs network; runs only with PROJGEN_E2E=1.
func TestAdded_BuildAndTest(t *testing.T) {
	if os.Getenv("PROJGEN_E2E") != "1" {
		t.Skip("set PROJGEN_E2E=1 to build and test projects with entities")
	}
	for _, o := range combos() {
		t.Run(o.Framework+"-"+o.Architecture+"-"+o.Database, func(t *testing.T) {
			dir := newProject(t, o)
			mustAdd(t, dir, "Product", testFields)
			mustAdd(t, dir, "order_item", []string{"quantity:int", "note:string"})
			goRun(t, dir, nil, "mod", "tidy")
			goRun(t, dir, nil, "vet", "./...")
			goRun(t, dir, nil, "test", "./...")
		})
	}
}

// TestAdded_RealDatabase runs the generated repository tests against real
// databases: PROJGEN_TEST_POSTGRES_URL and PROJGEN_TEST_MYSQL_URL (CI starts
// both as services). Each is skipped when its variable is unset.
func TestAdded_RealDatabase(t *testing.T) {
	urls := map[string]string{
		"postgresql": os.Getenv("PROJGEN_TEST_POSTGRES_URL"),
		"mysql":      os.Getenv("PROJGEN_TEST_MYSQL_URL"),
	}
	for _, db := range []string{"postgresql", "mysql"} {
		t.Run(db, func(t *testing.T) {
			if urls[db] == "" {
				t.Skipf("set PROJGEN_TEST_%s_URL to test against a real %s", map[string]string{"postgresql": "POSTGRES", "mysql": "MYSQL"}[db], db)
			}
			o := options.Options{Name: "shop", Module: "example.com/shop", Language: "go",
				Framework: "gin", Architecture: "clean", Database: db}
			dir := newProject(t, o)
			mustAdd(t, dir, "Product", testFields)
			goRun(t, dir, nil, "mod", "tidy")
			goRun(t, dir, []string{"TEST_DATABASE_URL=" + urls[db]}, "test", "-count=1", "-run", "Repository", "-v", "./...")
		})
	}
}

func TestAdd_JavaProjectNotSupportedYet(t *testing.T) {
	o := options.Options{Name: "shop", Module: "com.techpartners.shop", Language: "java",
		Framework: "spring-boot", Architecture: "clean", Database: "postgresql"}
	dir := newProject(t, o)
	spec, _ := Parse("Product", testFields)
	if _, err := Add(dir, spec, false, time.Now()); err == nil || !strings.Contains(err.Error(), "Go projects only") {
		t.Fatalf("err = %v, want a not-supported error", err)
	}
}
