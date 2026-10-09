// Package versions keeps the technology versions used in generated projects
// up to date by reading them from each technology's official source:
//
//   - Go:             go.dev/dl (official Go downloads)
//   - Go modules:     proxy.golang.org (official Go module mirror)
//   - Docker images:  hub.docker.com official images (library/*)
//   - GitHub Actions: api.github.com latest releases
//
// Fetched versions are cached in the user config dir. Built-in defaults,
// compiled into the binary, are used when the cache is missing and the
// network is unavailable, so projgen also works offline.
package versions

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Module paths whose latest versions are tracked.
const (
	Gin   = "github.com/gin-gonic/gin"
	Echo  = "github.com/labstack/echo/v4"
	Fiber = "github.com/gofiber/fiber/v2"
	Pgx   = "github.com/jackc/pgx/v5"
	MySQL = "github.com/go-sql-driver/mysql"
)

// Modules lists every tracked Go module.
var Modules = []string{Gin, Echo, Fiber, Pgx, MySQL}

// Actions lists every tracked GitHub Action.
var Actions = []string{"actions/checkout", "actions/setup-go"}

// Versions is one snapshot of technology versions.
type Versions struct {
	FetchedAt time.Time         `json:"fetched_at,omitzero"`
	Go        string            `json:"go"`      // major.minor, e.g. "1.27"
	Modules   map[string]string `json:"modules"` // module path -> "v1.2.3"
	Images    map[string]string `json:"images"`  // "postgres" -> "18-alpine", "mysql" -> "9.7"
	Actions   map[string]string `json:"actions"` // "actions/checkout" -> "v7"
}

//go:embed defaults.json
var defaultsJSON []byte

// Defaults returns the versions compiled into the binary.
func Defaults() Versions {
	var v Versions
	if err := json.Unmarshal(defaultsJSON, &v); err != nil {
		panic("versions: bad defaults.json: " + err.Error())
	}
	return v
}

// CachePath is where fetched versions are stored.
func CachePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projgen", "versions.json"), nil
}

// Load returns the cached versions merged over the defaults.
// cached is false when no cache exists yet.
func Load() (v Versions, cached bool) {
	v = Defaults()
	path, err := CachePath()
	if err != nil {
		return v, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return v, false
	}
	var c Versions
	if err := json.Unmarshal(b, &c); err != nil {
		return v, false
	}
	return Merge(v, c), true
}

// Save writes v to the cache.
func Save(v Versions) error {
	path, err := CachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// Merge returns base with every non-empty value of over applied on top.
func Merge(base, over Versions) Versions {
	out := Versions{
		FetchedAt: base.FetchedAt,
		Go:        base.Go,
		Modules:   maps.Clone(base.Modules),
		Images:    maps.Clone(base.Images),
		Actions:   maps.Clone(base.Actions),
	}
	if out.Modules == nil {
		out.Modules = map[string]string{}
	}
	if out.Images == nil {
		out.Images = map[string]string{}
	}
	if out.Actions == nil {
		out.Actions = map[string]string{}
	}
	if !over.FetchedAt.IsZero() {
		out.FetchedAt = over.FetchedAt
	}
	if over.Go != "" {
		out.Go = over.Go
	}
	maps.Copy(out.Modules, over.Modules)
	maps.Copy(out.Images, over.Images)
	maps.Copy(out.Actions, over.Actions)
	return out
}

// Fetcher reads the latest versions from the official sources.
// The base URLs can be replaced in tests.
type Fetcher struct {
	Client    *http.Client
	GoDL      string // https://go.dev/dl/?mode=json
	Proxy     string // https://proxy.golang.org
	DockerHub string // https://hub.docker.com
	GitHub    string // https://api.github.com
}

// NewFetcher returns a Fetcher for the real official sources.
func NewFetcher() *Fetcher {
	return &Fetcher{
		Client:    &http.Client{Timeout: 10 * time.Second},
		GoDL:      "https://go.dev/dl/?mode=json",
		Proxy:     "https://proxy.golang.org",
		DockerHub: "https://hub.docker.com",
		GitHub:    "https://api.github.com",
	}
}

// Fetch asks every source in parallel. It returns what it could fetch and
// one error per source that failed; callers merge the result over what they
// already have, so a partial failure keeps the older value for that item.
func (f *Fetcher) Fetch(ctx context.Context) (Versions, error) {
	v := Versions{Modules: map[string]string{}, Images: map[string]string{}, Actions: map[string]string{}}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []error
	)
	do := func(name string, fn func() (string, error), set func(string)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			val, err := fn()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
				return
			}
			set(val)
		}()
	}

	do("go", func() (string, error) { return f.goVersion(ctx) }, func(s string) { v.Go = s })
	for _, m := range Modules {
		do(m, func() (string, error) { return f.moduleVersion(ctx, m) }, func(s string) { v.Modules[m] = s })
	}
	do("postgres image", func() (string, error) { return f.postgresTag(ctx) }, func(s string) { v.Images["postgres"] = s })
	do("mysql image", func() (string, error) { return f.mysqlLTSTag(ctx) }, func(s string) { v.Images["mysql"] = s })
	for _, a := range Actions {
		do(a, func() (string, error) { return f.actionMajor(ctx, a) }, func(s string) { v.Actions[a] = s })
	}
	wg.Wait()

	if len(errs) < 1+len(Modules)+2+len(Actions) {
		v.FetchedAt = time.Now().UTC()
	}
	slices.SortFunc(errs, func(a, b error) int { return strings.Compare(a.Error(), b.Error()) })
	return v, errors.Join(errs...)
}

func (f *Fetcher) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "projgen")
	// In CI the anonymous GitHub API limit (60/hour per IP) is shared by many
	// jobs; use the workflow token when one is provided. Sent to GitHub only.
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" && f.GitHub != "" && strings.HasPrefix(url, f.GitHub+"/") {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := f.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(out)
}

var goRe = regexp.MustCompile(`^go(\d+)\.(\d+)(\.\d+)?$`)

// goVersion returns the newest stable Go release as "major.minor".
func (f *Fetcher) goVersion(ctx context.Context) (string, error) {
	var releases []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := f.getJSON(ctx, f.GoDL, &releases); err != nil {
		return "", err
	}
	for _, r := range releases {
		if m := goRe.FindStringSubmatch(r.Version); r.Stable && m != nil {
			return m[1] + "." + m[2], nil
		}
	}
	return "", errors.New("no stable release listed")
}

var semverRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// moduleVersion returns the latest release of a module from the Go module proxy.
func (f *Fetcher) moduleVersion(ctx context.Context, module string) (string, error) {
	var info struct{ Version string }
	if err := f.getJSON(ctx, f.Proxy+"/"+module+"/@latest", &info); err != nil {
		return "", err
	}
	if !semverRe.MatchString(info.Version) {
		return "", fmt.Errorf("unexpected version %q", info.Version)
	}
	return info.Version, nil
}

type hubTag struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

func (f *Fetcher) hubTags(ctx context.Context, image, filter string) ([]hubTag, error) {
	var page struct{ Results []hubTag }
	url := f.DockerHub + "/v2/repositories/library/" + image + "/tags?page_size=100"
	if filter != "" {
		url += "&name=" + filter
	}
	err := f.getJSON(ctx, url, &page)
	return page.Results, err
}

var pgAlpineRe = regexp.MustCompile(`^(\d+)-alpine$`)

// postgresTag returns the newest stable "<major>-alpine" tag of the official image.
func (f *Fetcher) postgresTag(ctx context.Context) (string, error) {
	tags, err := f.hubTags(ctx, "postgres", "alpine")
	if err != nil {
		return "", err
	}
	best := -1
	for _, t := range tags {
		if m := pgAlpineRe.FindStringSubmatch(t.Name); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > best {
				best = n
			}
		}
	}
	if best < 0 {
		return "", errors.New("no <major>-alpine tag found")
	}
	return strconv.Itoa(best) + "-alpine", nil
}

var majorMinorRe = regexp.MustCompile(`^\d+\.\d+$`)

// mysqlLTSTag returns the "major.minor" tag that the official "lts" tag points to.
func (f *Fetcher) mysqlLTSTag(ctx context.Context) (string, error) {
	tags, err := f.hubTags(ctx, "mysql", "")
	if err != nil {
		return "", err
	}
	var lts string
	for _, t := range tags {
		if t.Name == "lts" {
			lts = t.Digest
		}
	}
	if lts == "" {
		return "", errors.New("no lts tag found")
	}
	for _, t := range tags {
		if majorMinorRe.MatchString(t.Name) && t.Digest == lts {
			return t.Name, nil
		}
	}
	return "", errors.New("no major.minor tag matches lts")
}

var actionTagRe = regexp.MustCompile(`^(v\d+)(\.\d+)*$`)

// actionMajor returns the major version tag ("v7") of a GitHub Action's latest release.
func (f *Fetcher) actionMajor(ctx context.Context, repo string) (string, error) {
	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := f.getJSON(ctx, f.GitHub+"/repos/"+repo+"/releases/latest", &rel); err != nil {
		return "", err
	}
	m := actionTagRe.FindStringSubmatch(rel.TagName)
	if m == nil {
		return "", fmt.Errorf("unexpected tag %q", rel.TagName)
	}
	return m[1], nil
}
