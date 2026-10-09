package generator

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

func javaOpts(arch, db string, extras ...string) options.Options {
	return options.Options{
		Name:         "order-service",
		Module:       "com.techpartners.orderservice",
		Language:     "java",
		Framework:    "spring-boot",
		Architecture: arch,
		Database:     db,
		Extras:       extras,
	}
}

// javaCombos returns every architecture x database combination for Java.
func javaCombos() []options.Options {
	var out []options.Options
	for _, a := range options.Architectures {
		for _, d := range options.Databases {
			out = append(out, javaOpts(a.Value, d.Value, allExtras...))
		}
	}
	return out
}

func TestRenderJava_AllCombinations(t *testing.T) {
	mvnw, _ := fs.ReadFile(templateFS, "static/java/mvnw")
	for _, o := range javaCombos() {
		t.Run(name(o), func(t *testing.T) {
			files, err := Render(o, testV)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			d := NewData(o, testV)
			base := "src/main/java/com/techpartners/orderservice"
			want := []string{
				"pom.xml", "mvnw", "mvnw.cmd", ".mvn/wrapper/maven-wrapper.properties", ".gitattributes",
				".gitignore", ".env.example", "README.md", "project.yaml",
				base + "/OrderServiceApplication.java",
				d.P["http"].Dir + "/RequestIdFilter.java",
				d.P["service"].Dir + "/GreeterService.java",
				"src/main/resources/log4j2.xml",
				"src/test/java/com/techpartners/orderservice/OrderServiceApplicationTests.java",
				d.P["http"].TestDir + "/HelloControllerTest.java",
				"Dockerfile", "docker-compose.yml", ".gitlab-ci.yml", ".github/workflows/ci.yml",
			}
			for _, w := range want {
				if _, ok := files[w]; !ok {
					t.Errorf("missing %s", w)
				}
			}
			for p, b := range files {
				if strings.Contains(string(b), "<no value>") {
					t.Errorf("%s contains <no value>", p)
				}
				if strings.HasPrefix(p, "go.") || strings.HasSuffix(p, ".go") {
					t.Errorf("Go file %s in a Java project", p)
				}
			}
			if !bytes.Equal(files["mvnw"], mvnw) {
				t.Error("mvnw was changed; it must be copied as-is")
			}

			pom := string(files["pom.xml"])
			for _, w := range []string{
				"<version>" + testV.Java[versions.JavaSpringBoot] + "</version>",
				"<java.version>" + testV.Java[versions.JavaLTS] + "</java.version>",
				"spring-boot-starter-log4j2", "<artifactId>spring-boot-starter-logging</artifactId>",
				"springdoc-openapi-starter-webmvc-ui",
			} {
				if !strings.Contains(pom, w) {
					t.Errorf("pom.xml missing %q", w)
				}
			}
			if (o.Database != "none") != strings.Contains(pom, "spring-boot-starter-jdbc") {
				t.Errorf("pom.xml jdbc dependency does not match database=%s", o.Database)
			}
			if !strings.Contains(string(files[".mvn/wrapper/maven-wrapper.properties"]), "apache-maven-"+testV.Java[versions.JavaMaven]) {
				t.Error("maven-wrapper.properties does not use the Maven version")
			}
			// Every Java file declares the package that matches its folder.
			for p, b := range files {
				if !strings.HasSuffix(p, ".java") {
					continue
				}
				dir := filepath.ToSlash(filepath.Dir(p))
				dir = strings.TrimPrefix(strings.TrimPrefix(dir, "src/main/java/"), "src/test/java/")
				want := "package " + strings.ReplaceAll(dir, "/", ".") + ";"
				if !strings.Contains(string(b), want) {
					t.Errorf("%s does not declare %q", p, want)
				}
			}
		})
	}
}

func TestGenerateJava_MvnwExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not tracked on Windows")
	}
	dir := t.TempDir()
	if _, err := Generate(javaOpts("layered", "none"), testV, dir, Flags{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "mvnw"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("mvnw mode = %v, want executable", info.Mode())
	}
}

// TestGeneratedJava_BuildAndTest runs ./mvnw verify on every Java
// combination. Needs a JDK matching the Java LTS version and network access;
// runs only with PROJGEN_E2E_JAVA=1 (PROJGEN_E2E_LATEST=1 uses the latest
// versions from the official sources).
func TestGeneratedJava_BuildAndTest(t *testing.T) {
	if os.Getenv("PROJGEN_E2E_JAVA") != "1" {
		t.Skip("set PROJGEN_E2E_JAVA=1 to build and test generated Java projects")
	}
	v := testV
	if os.Getenv("PROJGEN_E2E_LATEST") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		got, err := versions.NewFetcher().Fetch(ctx)
		if err != nil {
			t.Fatalf("fetching latest versions: %v", err)
		}
		v = versions.Merge(testV, got)
	}
	t.Logf("java %v", v.Java)

	for _, o := range javaCombos() {
		t.Run(name(o), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Generate(o, v, dir, Flags{}); err != nil {
				t.Fatal(err)
			}
			mvnw := filepath.Join(dir, "mvnw")
			if runtime.GOOS == "windows" {
				mvnw = filepath.Join(dir, "mvnw.cmd")
			}
			cmd := exec.Command(mvnw, "-B", "-q", "verify")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("mvnw verify: %v\n%s", err, tail(out, 6000))
			}
		})
	}
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}
