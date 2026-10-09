package entity

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

func javaCombos() []options.Options {
	var out []options.Options
	for _, a := range options.Architectures {
		for _, d := range options.Databases {
			o := options.Options{Name: "shop", Module: "com.techpartners.shop", Language: "java",
				Framework: "spring-boot", Architecture: a.Value, Database: d.Value}
			if d.Value != "none" {
				o.Extras = []string{"migrations"}
			}
			out = append(out, o)
		}
	}
	// A database without the migrations extra: the repository test applies the script itself.
	out = append(out, options.Options{Name: "shop", Module: "com.techpartners.shop", Language: "java",
		Framework: "spring-boot", Architecture: "layered", Database: "mysql"})
	return out
}

func javaName(o options.Options) string {
	n := o.Architecture + "-" + o.Database
	if !o.HasExtra("migrations") && o.Database != "none" {
		n += "-nomigrations"
	}
	return n
}

func TestAddJava_AllCombinations(t *testing.T) {
	for _, o := range javaCombos() {
		t.Run(javaName(o), func(t *testing.T) {
			dir := newProject(t, o)
			res := mustAdd(t, dir, "Product", testFields)
			if !res.Java || len(res.Manual) != 0 || len(res.Modified) != 0 {
				t.Errorf("java = %v, manual = %v, modified = %v; Spring needs no wiring", res.Java, res.Manual, res.Modified)
			}
			wantFiles := 11
			if o.Database != "none" {
				wantFiles = 14
			}
			if len(res.Created) != wantFiles {
				t.Errorf("created %d files, want %d: %v", len(res.Created), wantFiles, res.Created)
			}
			res2 := mustAdd(t, dir, "order_item", []string{"quantity:int", "note:string"})
			if len(res2.Skipped) != 3 {
				t.Errorf("second entity skipped = %v, want the 3 shared files", res2.Skipped)
			}

			d := generator.NewData(o, versions.Defaults())
			for _, f := range append(res.Created, res2.Created...) {
				b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
				if err != nil {
					t.Fatal(err)
				}
				s := string(b)
				if strings.Contains(s, "<no value>") {
					t.Errorf("%s contains <no value>", f)
				}
				if !strings.HasSuffix(f, ".java") {
					continue
				}
				pkg := filepath.ToSlash(filepath.Dir(f))
				pkg = strings.TrimPrefix(strings.TrimPrefix(pkg, "src/main/java/"), "src/test/java/")
				if want := "package " + strings.ReplaceAll(pkg, "/", ".") + ";"; !strings.HasPrefix(s, want) {
					t.Errorf("%s does not start with %q", f, want)
				}
			}

			controller, _ := os.ReadFile(filepath.Join(dir, d.P["http"].Dir, "ProductController.java"))
			if !strings.Contains(string(controller), `@RequestMapping("/api/v1/products")`) {
				t.Error("ProductController is not mapped to /api/v1/products")
			}
			memory := filepath.Join(dir, d.P["db"].Dir, "InMemoryProductRepository.java")
			if _, err := os.Stat(memory); (err == nil) != (o.Database == "none") {
				t.Errorf("in-memory repository in main = %v, want it only without a database", err == nil)
			}
			if o.Database != "none" {
				migrations, _ := filepath.Glob(filepath.Join(dir, "src", "main", "resources", "db", "migration", "V*__create_*.sql"))
				if len(migrations) != 2 {
					t.Errorf("Flyway migrations = %v, want one per entity", migrations)
				}
			}
		})
	}
}

func TestAddJava_ReservedNames(t *testing.T) {
	dir := newProject(t, javaCombos()[0])
	for _, tt := range []struct {
		entity string
		fields []string
		want   string
	}{
		{"String", []string{"name:string"}, "Java class"},
		{"Product", []string{"class:string"}, "reserved in Java"},
		{"Product", []string{"hash_code:int"}, "reserved in Java"},
	} {
		spec, err := Parse(tt.entity, tt.fields)
		if err != nil {
			t.Fatalf("parse %s: %v", tt.entity, err)
		}
		if _, err := Add(dir, spec, false, time.Now()); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %v: err = %v, want %q", tt.entity, tt.fields, err, tt.want)
		}
	}
	// Fine in Go, so a Go project accepts the same names.
	spec, _ := Parse("Product", []string{"class:string"})
	if _, err := Add(newProject(t, combos()[0]), spec, false, time.Now()); err != nil {
		t.Errorf("Go project rejected a Java-only reserved name: %v", err)
	}
}

func TestMigrationStamp_NeverRepeats(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for _, java := range []bool{false, true} {
		o := combos()[0] // gin, layered, postgresql + migrations
		pattern := filepath.Join("migrations", "*_create_*.up.sql")
		if java {
			o = javaCombos()[0]
			o.Database, o.Extras = "postgresql", []string{"migrations"}
			pattern = filepath.Join("src", "main", "resources", "db", "migration", "V*__create_*.sql")
		}
		dir := newProject(t, o)
		mustAdd(t, dir, "Product", testFields) // also at now
		spec, _ := Parse("order_item", []string{"quantity:int"})
		if _, err := Add(dir, spec, false, now); err != nil {
			t.Fatal(err)
		}
		files, _ := filepath.Glob(filepath.Join(dir, pattern))
		if len(files) != 2 {
			t.Fatalf("java=%v: migrations = %v", java, files)
		}
		version := func(f string) string { return strings.TrimPrefix(filepath.Base(f), "V")[:14] }
		if a, b := version(files[0]), version(files[1]); a == b {
			t.Errorf("java=%v: two migrations share a version: %v", java, files)
		}
	}
}

func mvnw(t *testing.T, dir string, env []string) {
	t.Helper()
	bin := filepath.Join(dir, "mvnw")
	if runtime.GOOS == "windows" {
		bin = filepath.Join(dir, "mvnw.cmd")
	}
	cmd := exec.Command(bin, "-B", "-q", "verify")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if len(out) > 6000 {
			out = out[len(out)-6000:]
		}
		t.Fatalf("mvnw verify: %v\n%s", err, out)
	}
}

// TestAddedJava_BuildAndTest runs ./mvnw verify on every Java combination
// with two entities. Needs a JDK for the Java LTS and network access; runs
// only with PROJGEN_E2E_JAVA=1.
func TestAddedJava_BuildAndTest(t *testing.T) {
	if os.Getenv("PROJGEN_E2E_JAVA") != "1" {
		t.Skip("set PROJGEN_E2E_JAVA=1 to build and test Java projects with entities")
	}
	for _, o := range javaCombos() {
		t.Run(javaName(o), func(t *testing.T) {
			dir := newProject(t, o)
			mustAdd(t, dir, "Product", testFields)
			mustAdd(t, dir, "order_item", []string{"quantity:int", "note:string"})
			mvnw(t, dir, nil)
		})
	}
}

// TestAddedJava_RealDatabase runs the generated JDBC repository tests
// against real databases: PROJGEN_TEST_POSTGRES_JDBC and
// PROJGEN_TEST_MYSQL_JDBC with PROJGEN_TEST_DB_USER and
// PROJGEN_TEST_DB_PASSWORD. Needs PROJGEN_E2E_JAVA=1 and a matching JDK.
func TestAddedJava_RealDatabase(t *testing.T) {
	if os.Getenv("PROJGEN_E2E_JAVA") != "1" {
		t.Skip("set PROJGEN_E2E_JAVA=1 to build and test Java projects with entities")
	}
	urls := map[string]string{
		"postgresql": os.Getenv("PROJGEN_TEST_POSTGRES_JDBC"),
		"mysql":      os.Getenv("PROJGEN_TEST_MYSQL_JDBC"),
	}
	for _, db := range []string{"postgresql", "mysql"} {
		// With Flyway the test migrates; without it the test runs the table's script.
		for _, flyway := range []bool{true, false} {
			t.Run(db+map[bool]string{true: "-flyway", false: "-script"}[flyway], func(t *testing.T) {
				if urls[db] == "" {
					t.Skipf("no JDBC URL for %s", db)
				}
				o := options.Options{Name: "shop", Module: "com.techpartners.shop", Language: "java",
					Framework: "spring-boot", Architecture: "clean", Database: db}
				if flyway {
					o.Extras = []string{"migrations"}
				}
				dir := newProject(t, o)
				mustAdd(t, dir, "Product", testFields)
				mvnw(t, dir, []string{
					"TEST_DATABASE_URL=" + urls[db],
					"TEST_DATABASE_USER=" + os.Getenv("PROJGEN_TEST_DB_USER"),
					"TEST_DATABASE_PASSWORD=" + os.Getenv("PROJGEN_TEST_DB_PASSWORD"),
				})
			})
		}
	}
}
