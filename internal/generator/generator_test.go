package generator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

var testV = versions.Defaults()

// Versions from the official sources must end up in the generated files.
func TestRender_UsesVersions(t *testing.T) {
	v := versions.Defaults()
	v.Go = "1.99"
	v.Modules[versions.Echo] = "v4.99.0"
	v.Modules[versions.Pgx] = "v5.99.0"
	v.Images["postgres"] = "99-alpine"
	v.Actions["actions/checkout"] = "v99"

	files, err := Render(opts("echo", "clean", "postgresql", allExtras...), v)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"go.mod":                   {"go 1.99", versions.Echo + " v4.99.0", versions.Pgx + " v5.99.0"},
		"Dockerfile":               {"golang:1.99-alpine"},
		".gitlab-ci.yml":           {"golang:1.99"},
		"docker-compose.yml":       {"image: postgres:99-alpine"},
		".github/workflows/ci.yml": {"actions/checkout@v99"},
	}
	for file, wants := range checks {
		for _, w := range wants {
			if !strings.Contains(string(files[file]), w) {
				t.Errorf("%s does not contain %q:\n%s", file, w, files[file])
			}
		}
	}
	if strings.Contains(string(files["go.mod"]), versions.Gin) {
		t.Error("go.mod requires gin although echo was chosen")
	}
}

func TestRender_NetHTTPNoDBHasNoRequires(t *testing.T) {
	files, err := Render(opts("nethttp", "layered", "none"), testV)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files["go.mod"]), "require") {
		t.Errorf("go.mod should have no requires:\n%s", files["go.mod"])
	}
}

func opts(fw, arch, db string, extras ...string) options.Options {
	return options.Options{
		Name:         "demo-service",
		Module:       "example.com/demo-service",
		Language:     "go",
		Framework:    fw,
		Architecture: arch,
		Database:     db,
		Extras:       extras,
	}
}

var allExtras = []string{"docker", "docker-compose", "gitlab-ci", "github-actions", "swagger", "auth", "observability", "redis", "sonarqube",
	"tp-logger", "qpay", "bonum", "golomt"}

// extrasFor returns every extra valid for the database: migrations need one.
func extrasFor(db string) []string {
	if db == "none" {
		return allExtras
	}
	return append(slices.Clone(allExtras), "migrations")
}

// combos returns every framework x architecture x database combination.
func combos() []options.Options {
	var out []options.Options
	for _, fw := range options.Frameworks["go"] {
		for _, a := range options.Architectures {
			for _, d := range options.Databases {
				out = append(out, opts(fw.Value, a.Value, d.Value, extrasFor(d.Value)...))
			}
		}
	}
	return out
}

func name(o options.Options) string { return o.Framework + "-" + o.Architecture + "-" + o.Database }

// Every combination renders, and every .go file is valid, formatted Go.
func TestRender_AllCombinations(t *testing.T) {
	for _, o := range combos() {
		t.Run(name(o), func(t *testing.T) {
			files, err := Render(o, testV)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			d := NewData(o, testV)
			want := []string{
				"go.mod", "cmd/server/main.go", "README.md", "Makefile", ".gitignore", ".env.example", "project.yaml",
				d.P["http"].Dir + "/router.go", d.P["http"].Dir + "/router_test.go",
				d.P["service"].Dir + "/greeter.go", d.P["logger"].Dir + "/logger.go",
				"Dockerfile", "docker-compose.yml", ".gitlab-ci.yml", ".github/workflows/ci.yml", "docs/openapi.yaml",
			}
			if o.Database != "none" {
				want = append(want, d.P["db"].Dir+"/db.go", d.P["db"].Dir+"/migrate.go", "migrations/migrations.go")
				// The first migration is named after the generation time, which
				// Render reads itself; match the shape, not this test's clock.
				initRe := regexp.MustCompile(`^migrations/\d{14}_init\.(up|down)\.sql$`)
				n := 0
				for p := range files {
					if initRe.MatchString(p) {
						n++
					}
				}
				if n != 2 {
					t.Errorf("found %d init migration files, want up and down", n)
				}
				if !strings.Contains(string(files["cmd/server/main.go"]), ".Migrate(cfg.DatabaseURL)") {
					t.Error("main.go does not run migrations")
				}
				if !strings.Contains(string(files["go.mod"]), versions.Migrate) {
					t.Error("go.mod does not require golang-migrate")
				}
			}
			for _, w := range want {
				if _, ok := files[w]; !ok {
					t.Errorf("missing %s", w)
				}
			}
			for p, b := range files {
				if strings.Contains(string(b), "<no value>") {
					t.Errorf("%s contains <no value>: a template field is missing", p)
				}
			}
			main := string(files["cmd/server/main.go"])
			if !strings.Contains(main, d.P["http"].Import) {
				t.Errorf("main.go does not import the http package %s", d.P["http"].Import)
			}
			if (o.Database != "none") != strings.Contains(main, "DatabaseURL") {
				t.Errorf("main.go database wiring does not match database=%s", o.Database)
			}
		})
	}
}

func TestRender_ExtrasOnlyWhenChosen(t *testing.T) {
	files, err := Render(opts("gin", "layered", "none"), testV)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"Dockerfile", "docker-compose.yml", ".gitlab-ci.yml", ".github/workflows/ci.yml", "docs/openapi.yaml", "sonar-project.properties", "internal/repository/db.go"} {
		if _, ok := files[p]; ok {
			t.Errorf("%s generated but not chosen", p)
		}
	}
}

// auth, observability and redis add their packages and modules only when chosen.
func TestRender_AuthObservabilityRedis(t *testing.T) {
	plain, err := Render(opts("echo", "clean", "postgresql"), testV)
	if err != nil {
		t.Fatal(err)
	}
	all, err := Render(opts("echo", "clean", "postgresql", "docker", "docker-compose", "swagger", "auth", "observability", "redis"), testV)
	if err != nil {
		t.Fatal(err)
	}
	files := []string{"pkg/auth/auth.go", "cmd/token/main.go", "internal/repository/cache/cache.go", "pkg/telemetry/telemetry.go"}
	modules := []string{versions.JWT, versions.Redis, versions.Prometheus, versions.Otel, versions.OtelSDK, versions.OtelOTLP}
	for _, f := range files {
		if _, ok := plain[f]; ok {
			t.Errorf("%s generated without its extra", f)
		}
		if _, ok := all[f]; !ok {
			t.Errorf("%s missing", f)
		}
	}
	for _, m := range modules {
		if strings.Contains(string(plain["go.mod"]), m) {
			t.Errorf("go.mod requires %s without its extra", m)
		}
		if !strings.Contains(string(all["go.mod"]), m+" "+testV.Modules[m]) {
			t.Errorf("go.mod does not require %s %s", m, testV.Modules[m])
		}
	}
	checks := map[string][]string{
		"cmd/server/main.go":               {"auth.NewVerifier(", "telemetry.SetupTracing(", "cache.Open(", "HealthChecks{conn, redisCache}"},
		"internal/delivery/http/router.go": {"requireAuth(d.Auth)", "/metrics", `v1.GET("/me", me)`},
		"docker-compose.yml":               {"image: redis:" + testV.Images["redis"], "JWT_SECRET: ${JWT_SECRET:?"},
		".env.example":                     {"JWT_SECRET=\n", "REDIS_URL=", "OTEL_EXPORTER_OTLP_ENDPOINT="},
		"docs/openapi.yaml":                {"bearerAuth"},
	}
	for f, wants := range checks {
		for _, w := range wants {
			if !strings.Contains(string(all[f]), w) {
				t.Errorf("%s does not contain %q", f, w)
			}
		}
	}

	java, err := Render(javaOpts("clean", "postgresql", "auth", "observability", "redis"), testV)
	if err != nil {
		t.Fatal(err)
	}
	pom := string(java["pom.xml"])
	for _, w := range []string{"spring-boot-starter-security-oauth2-resource-server", "spring-boot-starter-opentelemetry",
		"micrometer-registry-prometheus", "spring-boot-starter-data-redis"} {
		if !strings.Contains(pom, w) {
			t.Errorf("pom.xml missing %s", w)
		}
	}
	base := "src/main/java/com/techpartners/orderservice"
	for _, f := range []string{base + "/config/SecurityConfig.java", base + "/config/CacheConfig.java",
		base + "/delivery/web/MeController.java", "src/test/java/com/techpartners/orderservice/ServerTests.java"} {
		if _, ok := java[f]; !ok {
			t.Errorf("missing %s", f)
		}
	}
}

// The TechPartners library extras add their module, package and settings
// only when chosen, and each payment client can be chosen on its own.
func TestRender_TechPartnersLibraries(t *testing.T) {
	plain, err := Render(opts("fiber", "hexagonal", "none"), testV)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{versions.TPLogger, versions.QPay, versions.Bonum, versions.Golomt} {
		if strings.Contains(string(plain["go.mod"]), m) {
			t.Errorf("go.mod requires %s without its extra", m)
		}
	}
	for p := range plain {
		if strings.Contains(p, "/payment/") {
			t.Errorf("%s generated without a payment extra", p)
		}
	}
	if strings.Contains(string(plain["pkg/logger/logger.go"]), "logger-go") {
		t.Error("logger uses logger-go without tp-logger")
	}

	dir := "internal/adapters/payment/"
	cases := []struct {
		extra, module string
		files         []string
		checks        map[string][]string
	}{
		{"tp-logger", versions.TPLogger, nil, map[string][]string{
			"pkg/logger/logger.go":      {`tplog "github.com/techpartners-asia/logger-go"`, `Time: "@timestamp", Level: "level", Message: "message"`, "tplog.WithRedaction()"},
			"pkg/logger/logger_test.go": {"TestSecretsRedacted"},
		}},
		{"qpay", versions.QPay, []string{dir + "doc.go", dir + "qpay.go", dir + "qpay_test.go"}, map[string][]string{
			"cmd/server/main.go":        {"payment.NewQPay(cfg.QPay)"},
			"internal/config/config.go": {`getenv("QPAY_ENDPOINT", "https://merchant-sandbox.qpay.mn/v2")`, "QPAY_INVOICE_CODE"},
			".env.example":              {"QPAY_USERNAME=\n", "QPAY_PASSWORD=\n"},
			"docker-compose.yml":        {"QPAY_PASSWORD: ${QPAY_PASSWORD:?"},
			"README.md":                 {"## Payments", "QPAY_USERNAME"},
		}},
		{"bonum", versions.Bonum, []string{dir + "doc.go", dir + "bonum.go", dir + "bonum_test.go"}, map[string][]string{
			"cmd/server/main.go":        {"payment.NewBonum(cfg.Bonum)", "defer bonumClient.Close()"},
			"internal/config/config.go": {`getenv("BONUM_ENV", "sandbox")`},
			".env.example":              {"BONUM_APP_SECRET=\n", "BONUM_CHECKSUM_KEY=\n"},
			"docker-compose.yml":        {"BONUM_APP_SECRET: ${BONUM_APP_SECRET:?"},
		}},
		{"golomt", versions.Golomt, []string{dir + "doc.go", dir + "golomt.go", dir + "golomt_test.go"}, map[string][]string{
			"cmd/server/main.go":        {"payment.NewGolomt(cfg.Golomt)"},
			"internal/config/config.go": {"func (g Golomt) Ecommerce() bool", "func (g Golomt) SocialPay() bool"},
			".env.example":              {"GOLOMT_ECOMMERCE_SECRET=\n", "GOLOMT_SOCIALPAY_SECRET=\n"},
			"docker-compose.yml":        {"GOLOMT_ECOMMERCE_TOKEN: ${GOLOMT_ECOMMERCE_TOKEN:-}"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.extra, func(t *testing.T) {
			files, err := Render(opts("fiber", "hexagonal", "none", "docker", "docker-compose", tc.extra), testV)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(files["go.mod"]), tc.module+" "+testV.Modules[tc.module]) {
				t.Errorf("go.mod does not require %s %s", tc.module, testV.Modules[tc.module])
			}
			for _, f := range tc.files {
				if _, ok := files[f]; !ok {
					t.Errorf("missing %s", f)
				}
			}
			for f, wants := range tc.checks {
				for _, w := range wants {
					if !strings.Contains(string(files[f]), w) {
						t.Errorf("%s does not contain %q", f, w)
					}
				}
			}
			// Credentials are only ever referenced, never given a value.
			for _, f := range []string{".env.example", "docker-compose.yml"} {
				for _, bad := range []string{"test-password", "test-secret", "test-token"} {
					if strings.Contains(string(files[f]), bad) {
						t.Errorf("%s contains the test value %q", f, bad)
					}
				}
			}
		})
	}

	if _, err := Render(javaOpts("clean", "postgresql", "qpay"), testV); err == nil || !strings.Contains(err.Error(), "Go projects only") {
		t.Errorf("java with qpay: err = %v, want a Go-only error", err)
	}
}

// sonarqube adds the scanner settings and, with a CI extra, a scan step that
// reads the server URL and token from CI secrets only.
func TestRender_SonarQube(t *testing.T) {
	goFiles, err := Render(opts("gin", "clean", "postgresql", "github-actions", "gitlab-ci", "sonarqube"), testV)
	if err != nil {
		t.Fatal(err)
	}
	javaFiles, err := Render(javaOpts("clean", "postgresql", "github-actions", "gitlab-ci", "sonarqube"), testV)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string][]string{
		"sonar-project.properties": {"sonar.projectKey=demo-service\n", "sonar.projectName=demo-service\n",
			"sonar.sources=.\n", "sonar.tests=.\n", "sonar.test.inclusions=**/*_test.go\n", "sonar.exclusions=**/*_test.go",
			"sonar.go.coverage.reportPaths=coverage.out\n"},
		".github/workflows/ci.yml": {"fetch-depth: 0", "go test -race -coverprofile=coverage.out ./...",
			"uses: SonarSource/sonarqube-scan-action@" + testV.Actions["SonarSource/sonarqube-scan-action"],
			"SONAR_TOKEN: ${{ secrets.SONAR_TOKEN }}", "SONAR_HOST_URL: ${{ vars.SONAR_HOST_URL || secrets.SONAR_HOST_URL }}",
			"if: env.HAS_SONAR_TOKEN == 'true'"},
		".gitlab-ci.yml": {"- analyze\n", "- coverage.out\n", "name: " + versions.SonarScannerImage + ":" + testV.Images[versions.SonarScannerImage],
			"- sonar-scanner\n", "needs: [test]", `GIT_DEPTH: "0"`, "- if: $SONAR_TOKEN\n"},
		"README.md":  {"## SonarQube", "`SONAR_TOKEN`", "`SONAR_HOST_URL`"},
		".gitignore": {".scannerwork/\n"},
	}
	javaChecks := map[string][]string{
		"pom.xml": {"<sonar.projectKey>order-service</sonar.projectKey>",
			"<sonar.coverage.jacoco.xmlReportPaths>${project.build.directory}/site/jacoco/jacoco.xml</sonar.coverage.jacoco.xmlReportPaths>",
			"<artifactId>jacoco-maven-plugin</artifactId>", "<version>" + testV.Java[versions.JavaJacoco] + "</version>",
			"<goal>prepare-agent</goal>", "<goal>report</goal>", "<phase>verify</phase>"},
		".github/workflows/ci.yml": {"fetch-depth: 0", "./mvnw -B org.sonarsource.scanner.maven:sonar-maven-plugin:sonar",
			"SONAR_TOKEN: ${{ secrets.SONAR_TOKEN }}", "if: env.HAS_SONAR_TOKEN == 'true'"},
		".gitlab-ci.yml": {"- analyze\n", "./mvnw -B verify org.sonarsource.scanner.maven:sonar-maven-plugin:sonar", "- if: $SONAR_TOKEN\n"},
		"README.md":      {"## SonarQube", "`SONAR_TOKEN`", "./mvnw verify org.sonarsource.scanner.maven:sonar-maven-plugin:sonar"},
	}
	for name, c := range map[string]struct {
		files  map[string][]byte
		checks map[string][]string
	}{"go": {goFiles, checks}, "java": {javaFiles, javaChecks}} {
		for f, wants := range c.checks {
			for _, w := range wants {
				if !strings.Contains(string(c.files[f]), w) {
					t.Errorf("%s %s does not contain %q", name, f, w)
				}
			}
		}
		// The token is only ever referenced, never written: no sonar.token or sonar.login setting.
		for f, b := range c.files {
			for _, bad := range []string{"<no value>", "sonar.token", "sonar.login", "sonar.password"} {
				if strings.Contains(string(b), bad) {
					t.Errorf("%s %s contains %q", name, f, bad)
				}
			}
		}
	}
	if _, ok := javaFiles["sonar-project.properties"]; ok {
		t.Error("java: sonar-project.properties generated; the Maven scanner reads pom.xml")
	}

	// Without the extra nothing about Sonar or JaCoCo is generated.
	plainGo, _ := Render(opts("gin", "clean", "postgresql", "github-actions", "gitlab-ci"), testV)
	plainJava, _ := Render(javaOpts("clean", "postgresql", "github-actions", "gitlab-ci"), testV)
	for name, files := range map[string]map[string][]byte{"go": plainGo, "java": plainJava} {
		for f, b := range files {
			if f == "project.yaml" {
				continue
			}
			for _, bad := range []string{"sonar", "Sonar", "jacoco"} {
				if strings.Contains(string(b), bad) {
					t.Errorf("%s %s mentions %q without the sonarqube extra", name, f, bad)
				}
			}
		}
	}
}

func TestRender_InvalidOptions(t *testing.T) {
	if _, err := Render(opts("spring-boot", "clean", "postgresql"), testV); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRender_ProjectYAMLRoundTrip(t *testing.T) {
	o := opts("echo", "hexagonal", "mysql", "docker")
	files, err := Render(o, testV)
	if err != nil {
		t.Fatal(err)
	}
	var back options.Options
	if err := yaml.Unmarshal(files["project.yaml"], &back); err != nil {
		t.Fatal(err)
	}
	if back.Framework != o.Framework || back.Architecture != o.Architecture || back.Database != o.Database || len(back.Extras) != 1 {
		t.Errorf("round trip mismatch: %+v", back)
	}
}

func TestGenerate_WritesFiles(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(opts("nethttp", "clean", "postgresql"), testV, dir, Flags{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.Files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("listed file not written: %s", f)
		}
	}
}

func TestGenerate_DryRunWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new")
	res, err := Generate(opts("gin", "layered", "none"), testV, dir, Flags{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		t.Error("dry run listed no files")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dry run created %s", dir)
	}
}

func TestGenerate_NonEmptyDir(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Generate(opts("gin", "layered", "none"), testV, dir, Flags{})
	if !errors.Is(err, ErrDirNotEmpty) {
		t.Fatalf("err = %v, want ErrDirNotEmpty", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); !os.IsNotExist(err) {
		t.Error("files written despite refusal")
	}

	if _, err := Generate(opts("gin", "layered", "none"), testV, dir, Flags{Force: true}); err != nil {
		t.Fatalf("force: %v", err)
	}
	if b, _ := os.ReadFile(keep); string(b) != "mine" {
		t.Error("force touched an unrelated file")
	}
}

// TestGenerated_BuildAndTest generates every combination, downloads
// dependencies and runs go vet + go test on the result. It needs network
// access and takes minutes, so it runs only with PROJGEN_E2E=1.
//
// With PROJGEN_E2E_LATEST=1 it first fetches the latest versions from the
// official sources, so a new release that breaks the templates is caught
// before developers hit it. Any source that cannot be reached fails the test.
func TestGenerated_BuildAndTest(t *testing.T) {
	if os.Getenv("PROJGEN_E2E") != "1" {
		t.Skip("set PROJGEN_E2E=1 to build and test generated projects")
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
	t.Logf("versions: go %s, modules %v, images %v, actions %v", v.Go, v.Modules, v.Images, v.Actions)

	for _, o := range combos() {
		t.Run(name(o), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Generate(o, v, dir, Flags{}); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"mod", "tidy"}, {"vet", "./..."}, {"test", "./..."}} {
				cmd := exec.Command("go", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
		})
	}
}

// TestGenerated_RedisCache runs the generated cache tests against a real
// Redis at PROJGEN_TEST_REDIS_URL (CI starts one as a service).
func TestGenerated_RedisCache(t *testing.T) {
	url := os.Getenv("PROJGEN_TEST_REDIS_URL")
	if url == "" {
		t.Skip("set PROJGEN_TEST_REDIS_URL to test the generated cache against a real Redis")
	}
	dir := t.TempDir()
	if _, err := Generate(opts("gin", "layered", "none", "redis"), testV, dir, Flags{}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"mod", "tidy"}, {"test", "-count=1", "-run", "TestCache", "-v", "./internal/cache/"}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "TEST_REDIS_URL="+url)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		if args[0] == "test" && strings.Contains(string(out), "SKIP") {
			t.Fatalf("cache test skipped:\n%s", out)
		}
	}
}
