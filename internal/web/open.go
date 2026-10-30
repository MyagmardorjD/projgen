package web

import (
	"errors"
	"os/exec"
	"runtime"
)

// vscodeApp is the macOS application name used when the code CLI is not on
// PATH: VS Code's "Install 'code' command in PATH" step is optional there.
const vscodeApp = "Visual Studio Code"

// appInstalled reports whether macOS knows an application called name.
// Replaced in tests.
var appInstalled = func(name string) bool {
	return exec.Command("open", "-Ra", name).Run() == nil
}

// openDir opens dir in VS Code ("code") or the file manager ("folder").
func openDir(dir, with string) error {
	var args []string
	if with == "code" {
		var ok bool
		if args, ok = codeCommand(runtime.GOOS, dir, exec.LookPath, appInstalled); !ok {
			return errors.New("VS Code (code) is not on PATH")
		}
	} else {
		args = openCommand(runtime.GOOS, dir)
	}
	return start(args)
}

// OpenBrowser opens url in the default browser.
func OpenBrowser(url string) error {
	if runtime.GOOS == "windows" {
		return start([]string{"rundll32", "url.dll,FileProtocolHandler", url})
	}
	return start(openCommand(runtime.GOOS, url))
}

// openCommand returns the command that opens target, a folder or URL, with
// the default handler on goos.
func openCommand(goos, target string) []string {
	switch goos {
	case "windows":
		return []string{"explorer", target}
	case "darwin":
		return []string{"open", target}
	default:
		return []string{"xdg-open", target}
	}
}

// codeCommand returns the command that opens dir in VS Code: the code CLI
// when it is on PATH, otherwise on macOS the installed app via open -a.
// ok is false when VS Code cannot be found.
func codeCommand(goos, dir string, lookPath func(string) (string, error), installed func(string) bool) (args []string, ok bool) {
	if code, err := lookPath("code"); err == nil {
		return []string{code, dir}, true
	}
	if goos == "darwin" && installed(vscodeApp) {
		return []string{"open", "-a", vscodeApp, dir}, true
	}
	return nil, false
}

// fileManager names the file manager on goos for the "open folder" button,
// or "" when there is no single well-known one.
func fileManager(goos string) string {
	switch goos {
	case "windows":
		return "Explorer"
	case "darwin":
		return "Finder"
	default:
		return ""
	}
}

// start runs args without waiting for the program to exit, and reaps it
// afterwards so no zombie process is left behind.
func start(args []string) error {
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
