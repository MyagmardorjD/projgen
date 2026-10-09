package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MyagmardorjD/projgen/internal/versions"
)

type env struct {
	t      *testing.T
	s      *Server
	srv    *httptest.Server
	opened []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	e := &env{t: t}
	e.s = New(versions.Defaults(), func(v versions.Versions) (versions.Versions, error) {
		v.Go = "1.99"
		return v, errors.New("github: down")
	})
	e.s.OpenDir = func(dir, with string) error {
		e.opened = append(e.opened, with+":"+dir)
		return nil
	}
	e.srv = httptest.NewServer(e.s.Handler())
	t.Cleanup(e.srv.Close)
	e.s.AllowHost(strings.TrimPrefix(e.srv.URL, "http://"))
	return e
}

func (e *env) post(path string, body any) *http.Response {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tokenHeader, e.s.Token())
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decodeBody[T any](t *testing.T, r *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func validReq(parent string) map[string]any {
	return map[string]any{
		"options": map[string]any{
			"name": "web-api", "module": "github.com/MyagmardorjD/web-api", "language": "go",
			"framework": "echo", "architecture": "hexagonal", "database": "mysql",
			"extras": []string{"docker", "docker-compose"},
		},
		"parent": parent,
	}
}

func TestIndexHasToken(t *testing.T) {
	e := newEnv(t)
	resp, err := e.srv.Client().Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), e.s.Token()) || strings.Contains(string(body), "__PROJGEN_TOKEN__") {
		t.Error("page does not carry the run token")
	}
}

func TestGuard(t *testing.T) {
	e := newEnv(t)

	t.Run("wrong host", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/options", nil)
		req.Host = "evil.example:80"
		resp, err := e.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})

	t.Run("post without token", func(t *testing.T) {
		resp, err := e.srv.Client().Post(e.srv.URL+"/api/create", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})
}

func TestOptions(t *testing.T) {
	e := newEnv(t)
	resp, err := e.srv.Client().Get(e.srv.URL + "/api/options")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := decodeBody[struct {
		Frameworks    map[string][]struct{ Value string }
		DefaultParent string `json:"default_parent"`
		Versions      struct {
			Rows []struct{ Name, Version string }
		}
	}](t, resp)
	if len(got.Frameworks["go"]) != 4 {
		t.Errorf("frameworks = %v", got.Frameworks)
	}
	if !strings.HasSuffix(filepath.ToSlash(got.DefaultParent), "source/repos") {
		t.Errorf("default parent = %s", got.DefaultParent)
	}
	if len(got.Versions.Rows) == 0 || got.Versions.Rows[0].Version == "" {
		t.Errorf("versions missing: %+v", got.Versions)
	}
}

func TestPreview(t *testing.T) {
	e := newEnv(t)
	parent := t.TempDir()
	resp := e.post("/api/preview", validReq(parent))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := decodeBody[struct {
		Dir   string
		Files []string
	}](t, resp)
	if got.Dir != filepath.Join(parent, "web-api") {
		t.Errorf("dir = %s", got.Dir)
	}
	if !strings.Contains(strings.Join(got.Files, ","), "internal/adapters/http/router.go") {
		t.Errorf("files = %v", got.Files)
	}
	if _, err := os.Stat(got.Dir); !os.IsNotExist(err) {
		t.Error("preview wrote to disk")
	}
}

func TestPreview_InvalidOptions(t *testing.T) {
	e := newEnv(t)
	req := validReq(t.TempDir())
	req["options"].(map[string]any)["extras"] = []string{"docker-compose"}
	resp := e.post("/api/preview", req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	got := decodeBody[struct{ Errors []string }](t, resp)
	if len(got.Errors) == 0 || !strings.Contains(got.Errors[0], "requires docker") {
		t.Errorf("errors = %v", got.Errors)
	}
}

func TestZip(t *testing.T) {
	e := newEnv(t)
	resp := e.post("/api/zip", validReq(""))
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("status = %d, type = %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"web-api/go.mod", "web-api/docker-compose.yml", "web-api/cmd/server/main.go"} {
		if !names[want] {
			t.Errorf("zip missing %s", want)
		}
	}
}

func TestCreateAndOpen(t *testing.T) {
	e := newEnv(t)
	parent := t.TempDir()
	resp := e.post("/api/create", validReq(parent))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	got := decodeBody[struct{ Dir string }](t, resp)
	want := filepath.Join(parent, "web-api")
	if got.Dir != want {
		t.Fatalf("dir = %s, want %s", got.Dir, want)
	}
	if _, err := os.Stat(filepath.Join(want, "go.mod")); err != nil {
		t.Fatalf("project not written: %v", err)
	}

	t.Run("second create conflicts", func(t *testing.T) {
		if r := e.post("/api/create", validReq(parent)); r.StatusCode != http.StatusConflict {
			t.Errorf("status = %d, want 409", r.StatusCode)
		}
	})

	t.Run("open created dir", func(t *testing.T) {
		if r := e.post("/api/open", map[string]string{"dir": want, "with": "folder"}); r.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", r.StatusCode)
		}
		if len(e.opened) != 1 || e.opened[0] != "folder:"+want {
			t.Errorf("opened = %v", e.opened)
		}
	})

	t.Run("open other dir refused", func(t *testing.T) {
		if r := e.post("/api/open", map[string]string{"dir": `C:\Windows`, "with": "folder"}); r.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", r.StatusCode)
		}
	})
}

func TestUpdateKeepsServingWithWarning(t *testing.T) {
	e := newEnv(t)
	resp := e.post("/api/update", map[string]any{})
	got := decodeBody[struct {
		Warning  string
		Versions struct {
			Rows []struct{ Name, Version string }
		}
	}](t, resp)
	if !strings.Contains(got.Warning, "github: down") {
		t.Errorf("warning = %q", got.Warning)
	}
	if got.Versions.Rows[0].Version != "1.99" {
		t.Errorf("go = %s, want refreshed 1.99", got.Versions.Rows[0].Version)
	}
}
