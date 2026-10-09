package versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeSources serves responses shaped like the real official sources.
func fakeSources(t *testing.T, broken map[string]bool) *Fetcher {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(key, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if broken[key] {
				http.Error(w, "down", http.StatusServiceUnavailable)
				return
			}
			w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/dl", reply("go", `[{"version":"go1.30rc1","stable":false},{"version":"go1.29.3","stable":true},{"version":"go1.28.9","stable":true}]`))
	for _, m := range Modules {
		mux.HandleFunc("/proxy/"+m+"/@latest", reply(m, `{"Version":"v9.8.7","Time":"2026-01-01T00:00:00Z"}`))
	}
	mux.HandleFunc("/hub/v2/repositories/library/postgres/tags", reply("postgres",
		`{"results":[{"name":"20beta1-alpine"},{"name":"19.2-alpine"},{"name":"19-alpine"},{"name":"18-alpine"},{"name":"alpine"},{"name":"19-alpine3.24"}]}`))
	mux.HandleFunc("/hub/v2/repositories/library/redis/tags", reply("redis",
		`{"results":[{"name":"9-rc1-alpine"},{"name":"8.8-alpine"},{"name":"8-alpine"},{"name":"7-alpine"},{"name":"alpine"}]}`))
	mux.HandleFunc("/hub/v2/repositories/library/mysql/tags", reply("mysql",
		`{"results":[{"name":"latest","digest":"sha256:inno"},{"name":"lts","digest":"sha256:lts"},{"name":"9.9.1","digest":"sha256:lts"},{"name":"9.9","digest":"sha256:lts"},{"name":"9","digest":"sha256:lts"},{"name":"8.4","digest":"sha256:old"},{"name":"10.1","digest":"sha256:inno"}]}`))
	for _, a := range Actions {
		mux.HandleFunc("/gh/repos/"+a+"/releases/latest", reply(a, `{"tag_name":"v8.1.0"}`))
	}
	mux.HandleFunc("/adoptium/v3/info/available_releases", reply("java lts", `{"most_recent_lts":29,"most_recent_feature_release":31}`))
	for _, path := range JavaArtifacts {
		mux.HandleFunc("/maven/"+path+"/maven-metadata.xml", reply(path,
			`<metadata><versioning><release>8.0.0-M1</release><versions><version>7.9.1</version><version>7.10.0</version><version>8.0.0-M1</version><version>7.2.15</version></versions></versioning></metadata>`))
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Fetcher{
		Client:    srv.Client(),
		GoDL:      srv.URL + "/dl",
		Proxy:     srv.URL + "/proxy",
		DockerHub: srv.URL + "/hub",
		GitHub:    srv.URL + "/gh",
		Adoptium:  srv.URL + "/adoptium",
		Maven:     srv.URL + "/maven",
	}
}

func TestFetch(t *testing.T) {
	v, err := fakeSources(t, nil).Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Go != "1.29" {
		t.Errorf("go = %q, want newest stable 1.29 (rc skipped)", v.Go)
	}
	for _, m := range Modules {
		if v.Modules[m] != "v9.8.7" {
			t.Errorf("%s = %q", m, v.Modules[m])
		}
	}
	if v.Images["postgres"] != "19-alpine" {
		t.Errorf("postgres = %q, want 19-alpine (beta skipped)", v.Images["postgres"])
	}
	if v.Images["redis"] != "8-alpine" {
		t.Errorf("redis = %q, want 8-alpine (rc skipped)", v.Images["redis"])
	}
	if v.Images["mysql"] != "9.9" {
		t.Errorf("mysql = %q, want 9.9 (the lts tag)", v.Images["mysql"])
	}
	for _, a := range Actions {
		if v.Actions[a] != "v8" {
			t.Errorf("%s = %q, want v8", a, v.Actions[a])
		}
	}
	if v.Java[JavaLTS] != "29" {
		t.Errorf("java lts = %q, want 29", v.Java[JavaLTS])
	}
	for key := range JavaArtifacts {
		if v.Java[key] != "7.10.0" {
			t.Errorf("java %s = %q, want 7.10.0 (highest stable, numeric order, milestone skipped)", key, v.Java[key])
		}
	}
	if v.FetchedAt.IsZero() {
		t.Error("FetchedAt not set")
	}
}

func TestFetch_PartialFailureKeepsOldValues(t *testing.T) {
	got, err := fakeSources(t, map[string]bool{"go": true, "mysql": true}).Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "go:") || !strings.Contains(err.Error(), "mysql image") {
		t.Fatalf("err = %v, want go and mysql failures", err)
	}

	merged := Merge(Defaults(), got)
	if merged.Go != Defaults().Go {
		t.Errorf("go = %q, want default %q kept", merged.Go, Defaults().Go)
	}
	if merged.Images["mysql"] != Defaults().Images["mysql"] {
		t.Errorf("mysql = %q, want default kept", merged.Images["mysql"])
	}
	if merged.Images["postgres"] != "19-alpine" {
		t.Errorf("postgres = %q, want fetched value", merged.Images["postgres"])
	}
}

func TestFetch_AllFail(t *testing.T) {
	broken := map[string]bool{"go": true, "postgres": true, "redis": true, "mysql": true, "java lts": true}
	for _, path := range JavaArtifacts {
		broken[path] = true
	}
	for _, m := range Modules {
		broken[m] = true
	}
	for _, a := range Actions {
		broken[a] = true
	}
	v, err := fakeSources(t, broken).Fetch(context.Background())
	if err == nil {
		t.Fatal("expected errors")
	}
	if !v.FetchedAt.IsZero() {
		t.Error("FetchedAt set although nothing was fetched")
	}
}

func TestDefaultsComplete(t *testing.T) {
	d := Defaults()
	if d.Go == "" {
		t.Error("default go version missing")
	}
	for _, m := range Modules {
		if d.Modules[m] == "" {
			t.Errorf("default for %s missing", m)
		}
	}
	for _, img := range []string{"postgres", "mysql"} {
		if d.Images[img] == "" {
			t.Errorf("default image %s missing", img)
		}
	}
	for _, a := range Actions {
		if d.Actions[a] == "" {
			t.Errorf("default for %s missing", a)
		}
	}
	for _, k := range JavaKeys {
		if d.Java[k] == "" {
			t.Errorf("default java %s missing", k)
		}
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)         // Windows
	t.Setenv("XDG_CONFIG_HOME", dir) // Linux
	t.Setenv("HOME", dir)            // macOS

	if _, cached := Load(); cached {
		t.Fatal("cache reported before anything was saved")
	}
	v := Defaults()
	v.Go = "1.99"
	if err := Save(v); err != nil {
		t.Fatal(err)
	}
	got, cached := Load()
	if !cached || got.Go != "1.99" {
		t.Errorf("Load = %q, cached %v; want 1.99 from cache", got.Go, cached)
	}
}

func TestFetch_GitHubTokenOnlySentToGitHub(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token")
	var mu sync.Mutex
	auth := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth[strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]] = r.Header.Get("Authorization")
		mu.Unlock()
		http.Error(w, "nothing here", http.StatusNotFound)
	}))
	defer srv.Close()
	f := &Fetcher{Client: srv.Client(), GoDL: srv.URL + "/dl", Proxy: srv.URL + "/proxy", DockerHub: srv.URL + "/hub", GitHub: srv.URL + "/gh",
		Adoptium: srv.URL + "/adoptium", Maven: srv.URL + "/maven"}
	_, _ = f.Fetch(context.Background())

	if auth["gh"] != "Bearer test-token" {
		t.Errorf("GitHub request auth = %q, want the token", auth["gh"])
	}
	for _, src := range []string{"dl", "proxy", "hub", "adoptium", "maven"} {
		if auth[src] != "" {
			t.Errorf("token leaked to %s", src)
		}
	}
}

func TestLatestProjgen(t *testing.T) {
	for _, tt := range []struct {
		status     int
		body, want string
		wantErr    bool
	}{
		{http.StatusOK, `{"tag_name":"v0.3.1"}`, "v0.3.1", false},
		{http.StatusOK, `{"tag_name":"nightly"}`, "", true},
		{http.StatusNotFound, `{}`, "", true}, // no release yet
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/"+ProjgenRepo+"/releases/latest" {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(tt.status)
			w.Write([]byte(tt.body))
		}))
		got, err := (&Fetcher{Client: srv.Client(), GitHub: srv.URL}).LatestProjgen(context.Background())
		srv.Close()
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%s: got %q, %v", tt.body, got, err)
		}
	}
}
