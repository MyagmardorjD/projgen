package web

import (
	"errors"
	"slices"
	"testing"
)

func TestOpenCommand(t *testing.T) {
	tests := map[string][]string{
		"windows": {"explorer", "/p"},
		"darwin":  {"open", "/p"},
		"linux":   {"xdg-open", "/p"},
		"freebsd": {"xdg-open", "/p"},
	}
	for goos, want := range tests {
		if got := openCommand(goos, "/p"); !slices.Equal(got, want) {
			t.Errorf("openCommand(%s) = %v, want %v", goos, got, want)
		}
	}
}

func TestCodeCommand(t *testing.T) {
	onPath := func(string) (string, error) { return "/usr/local/bin/code", nil }
	notOnPath := func(string) (string, error) { return "", errors.New("not found") }
	installed := func(name string) bool { return name == vscodeApp }
	notInstalled := func(string) bool { return false }

	tests := []struct {
		name      string
		goos      string
		lookPath  func(string) (string, error)
		installed func(string) bool
		want      []string
	}{
		{"cli on PATH", "darwin", onPath, installed, []string{"/usr/local/bin/code", "/p"}},
		{"cli on PATH, linux", "linux", onPath, notInstalled, []string{"/usr/local/bin/code", "/p"}},
		{"macOS app without cli", "darwin", notOnPath, installed, []string{"open", "-a", vscodeApp, "/p"}},
		{"macOS without VS Code", "darwin", notOnPath, notInstalled, nil},
		{"windows without cli", "windows", notOnPath, installed, nil},
		{"linux without cli", "linux", notOnPath, installed, nil},
	}
	for _, tt := range tests {
		got, ok := codeCommand(tt.goos, "/p", tt.lookPath, tt.installed)
		if ok != (tt.want != nil) || !slices.Equal(got, tt.want) {
			t.Errorf("%s: codeCommand = %v, %v; want %v", tt.name, got, ok, tt.want)
		}
	}
}

func TestFileManager(t *testing.T) {
	for goos, want := range map[string]string{"darwin": "Finder", "windows": "Explorer", "linux": ""} {
		if got := fileManager(goos); got != want {
			t.Errorf("fileManager(%s) = %q, want %q", goos, got, want)
		}
	}
}
