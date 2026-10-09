// Package web serves the projgen UI: a local page where a developer picks
// technologies, previews the files, and either downloads a ZIP or has the
// project created directly on their computer.
//
// The server is meant to listen on 127.0.0.1 only. Because it can write to
// disk, every request must use an allowed Host (blocks DNS rebinding) and
// every POST must carry the per-run token embedded in the page (blocks other
// websites from posting to localhost).
package web

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/MyagmardorjD/projgen/internal/create"
	"github.com/MyagmardorjD/projgen/internal/generator"
	"github.com/MyagmardorjD/projgen/internal/options"
	"github.com/MyagmardorjD/projgen/internal/prompt"
	"github.com/MyagmardorjD/projgen/internal/versions"
)

//go:embed index.html
var indexHTML string

const tokenHeader = "X-Projgen-Token"

// Server holds the UI state for one projgen serve run.
type Server struct {
	token   string
	hosts   map[string]bool
	refresh func(versions.Versions) (versions.Versions, error)

	mu      sync.Mutex
	v       versions.Versions
	created map[string]bool // directories created in this run; only these can be opened

	// OpenDir opens dir in VS Code ("code") or the file manager ("folder").
	// Replaced in tests.
	OpenDir func(dir, with string) error
}

// New returns a server that generates with v and uses refresh for the
// "check for updates" button.
func New(v versions.Versions, refresh func(versions.Versions) (versions.Versions, error)) *Server {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return &Server{
		token:   hex.EncodeToString(b),
		hosts:   map[string]bool{},
		refresh: refresh,
		v:       v,
		created: map[string]bool{},
		OpenDir: openDir,
	}
}

// AllowHost adds a Host header value the server answers to, e.g. "127.0.0.1:8090".
func (s *Server) AllowHost(host string) { s.hosts[host] = true }

// Token returns the per-run token. Only the page and tests need it.
func (s *Server) Token() string { return s.token }

// Handler returns the HTTP handler with every route.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/options", s.options)
	mux.HandleFunc("POST /api/preview", s.preview)
	mux.HandleFunc("POST /api/zip", s.zip)
	mux.HandleFunc("POST /api/create", s.create)
	mux.HandleFunc("POST /api/update", s.update)
	mux.HandleFunc("POST /api/open", s.open)
	return s.guard(mux)
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.hosts[r.Host] {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), []byte(s.token)) != 1 {
				http.Error(w, "missing or wrong token", http.StatusForbidden)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) index(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'")
	io.WriteString(w, strings.Replace(indexHTML, "__PROJGEN_TOKEN__", s.token, 1))
}

type versionRow struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Source  string `json:"source"`
}

type versionsResp struct {
	FetchedAt string       `json:"fetched_at"`
	Rows      []versionRow `json:"rows"`
}

func rows(v versions.Versions) versionsResp {
	r := versionsResp{}
	if !v.FetchedAt.IsZero() {
		r.FetchedAt = v.FetchedAt.Local().Format("2006-01-02 15:04")
	}
	add := func(name, ver, src string) { r.Rows = append(r.Rows, versionRow{name, ver, src}) }
	add("Go", v.Go, "go.dev")
	for _, m := range versions.Modules {
		add(m, v.Modules[m], "proxy.golang.org")
	}
	add("postgres image", v.Images["postgres"], "Docker Hub")
	add("mysql LTS image", v.Images["mysql"], "Docker Hub")
	for _, a := range versions.Actions {
		add(a, v.Actions[a], "GitHub")
	}
	add("Java LTS", v.Java[versions.JavaLTS], "api.adoptium.net")
	for _, k := range versions.JavaKeys[1:] {
		add(k, v.Java[k], "Maven Central")
	}
	return r
}

func (s *Server) current() versions.Versions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.v
}

func (s *Server) options(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"languages":      options.Languages,
		"frameworks":     options.Frameworks,
		"architectures":  options.Architectures,
		"databases":      options.Databases,
		"extras":         options.Extras,
		"default_parent": create.DefaultParent(),
		"separator":      string(filepath.Separator),
		"versions":       rows(s.current()),
	})
}

type request struct {
	Options options.Options `json:"options"`
	Parent  string          `json:"parent"`
	Force   bool            `json:"force"`
	Tidy    bool            `json:"tidy"`
	Git     bool            `json:"git"`
}

// decode reads the request and validates its options, answering 400 itself.
func decode(w http.ResponseWriter, r *http.Request) (request, bool) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrors(w, http.StatusBadRequest, errors.New("invalid request body"))
		return req, false
	}
	if err := req.Options.Validate(); err != nil {
		writeErrors(w, http.StatusBadRequest, err)
		return req, false
	}
	return req, true
}

func (req request) dir() (string, error) {
	parent := strings.TrimSpace(req.Parent)
	if parent == "" {
		parent = create.DefaultParent()
	}
	return filepath.Abs(filepath.Join(prompt.ExpandHome(parent), req.Options.Name))
}

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	req, ok := decode(w, r)
	if !ok {
		return
	}
	files, err := generator.Render(req.Options, s.current())
	if err != nil {
		writeErrors(w, http.StatusInternalServerError, err)
		return
	}
	dir, err := req.dir()
	if err != nil {
		writeErrors(w, http.StatusBadRequest, err)
		return
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	entries, _ := os.ReadDir(dir)
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "files": names, "dir_not_empty": len(entries) > 0})
}

func (s *Server) zip(w http.ResponseWriter, r *http.Request) {
	req, ok := decode(w, r)
	if !ok {
		return
	}
	files, err := generator.Render(req.Options, s.current())
	if err != nil {
		writeErrors(w, http.StatusInternalServerError, err)
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: req.Options.Name + "/" + n, Method: zip.Deflate, Modified: time.Now()})
		if err == nil {
			_, err = f.Write(files[n])
		}
		if err != nil {
			writeErrors(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := zw.Close(); err != nil {
		writeErrors(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, req.Options.Name))
	w.Write(buf.Bytes())
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	req, ok := decode(w, r)
	if !ok {
		return
	}
	dir, err := req.dir()
	if err != nil {
		writeErrors(w, http.StatusBadRequest, err)
		return
	}
	var log bytes.Buffer
	res, warns, err := create.Project(req.Options, s.current(), dir,
		generator.Flags{Force: req.Force}, create.Steps{Tidy: req.Tidy, Git: req.Git}, &log)
	if errors.Is(err, generator.ErrDirNotEmpty) {
		writeErrors(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErrors(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	s.created[dir] = true
	s.mu.Unlock()

	warnings := []string{}
	for _, wn := range warns {
		warnings = append(warnings, fmt.Sprintf("%s failed: %v", wn.Step, wn.Err))
	}
	_, hasCode := exec.LookPath("code")
	writeJSON(w, http.StatusOK, map[string]any{
		"dir":      dir,
		"files":    res.Files,
		"warnings": warnings,
		"log":      log.String(),
		"has_code": hasCode == nil,
	})
}

func (s *Server) update(w http.ResponseWriter, _ *http.Request) {
	v, err := s.refresh(s.current())
	s.mu.Lock()
	s.v = v
	s.mu.Unlock()
	resp := map[string]any{"versions": rows(v)}
	if err != nil {
		resp["warning"] = err.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dir  string `json:"dir"`
		With string `json:"with"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.With != "code" && req.With != "folder") {
		writeErrors(w, http.StatusBadRequest, errors.New("invalid request body"))
		return
	}
	s.mu.Lock()
	allowed := s.created[req.Dir]
	s.mu.Unlock()
	if !allowed {
		writeErrors(w, http.StatusForbidden, errors.New("only projects created in this session can be opened"))
		return
	}
	if err := s.OpenDir(req.Dir, req.With); err != nil {
		writeErrors(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func openDir(dir, with string) error {
	if with == "code" {
		code, err := exec.LookPath("code")
		if err != nil {
			return errors.New("VS Code (code) is not on PATH")
		}
		return exec.Command(code, dir).Start()
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer", dir).Start()
	case "darwin":
		return exec.Command("open", dir).Start()
	default:
		return exec.Command("xdg-open", dir).Start()
	}
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErrors(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string][]string{"errors": strings.Split(err.Error(), "\n")})
}
