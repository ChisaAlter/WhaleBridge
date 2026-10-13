package agent

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/ChisaAlter/WhaleBridge/internal/appdir"
	"github.com/ChisaAlter/WhaleBridge/internal/proc"
)

// An agent that is a desktop app as well as a CLI has the app's version shown
// beside the CLI's (#1334, laacmilan: with Codex's CLI and its app both
// installed, the Agents page showed only the CLI's version). Both read the
// same settings, so one row stands for both.

// desktopApp is the macOS bundle an agent's desktop app is known by: its
// bundle id, and the names it has been installed under. OpenAI's Codex app
// is ChatGPT.app (com.openai.codex) since it took ChatGPT's name; the bundle
// id tells it from the ChatGPT chat app (com.openai.chat) of the same name.
// win is its executable under %LOCALAPPDATA%\Programs, where a per-user
// installer puts it on Windows. store and publisher identify a Microsoft
// Store package by its package name and publisher id.
type desktopApp struct {
	id        string
	names     []string
	win       string
	store     string
	publisher string
}

var desktopApps = map[string]desktopApp{
	"codex": {id: "com.openai.codex", names: []string{"ChatGPT.app", "Codex.app"}, store: "OpenAI.Codex", publisher: "2p2nqsd0c76g0"},
	// DeepSeek Harness Desktop (0.2.0-rc.2's DeepSeek Harness.app,
	// com.deepseek.dsh) reads ~/.dsh as the CLI does — its own profile,
	// profiles/desktop, beside the CLI's — and needs no CLI: its terminal
	// command is optional (star on Discord: 未找到 CLI with the desktop app
	// in use). On Windows its installer (electron-builder's per-user NSIS,
	// productName DeepSeek Harness) puts it under %LOCALAPPDATA%\Programs.
	"dsh": {id: "com.deepseek.dsh", names: []string{"DeepSeek Harness.app"}, win: filepath.Join("DeepSeek Harness", "DeepSeek Harness.exe")},
}

// appFolders are where apps are installed; a var so tests can point it
// elsewhere: /Applications and ~/Applications on macOS, %LOCALAPPDATA%\Programs
// on Windows. Store packages are read from the current user's AppX registry.
var appFolders = func() []string {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return proc.SystemDirs("/Applications")
		}
		return macAppFolders(home)
	case "windows":
		if local := appdir.Getenv("LOCALAPPDATA"); local != "" {
			return []string{filepath.Join(local, "Programs")}
		}
	}
	return nil
}

// macAppFolders are the folders a Mac keeps apps in: /Applications and
// home's Applications. /Applications goes through proc.SystemDirs, so in a
// test binary testenv isolated it lies under the test's home: tests read
// the developer's own /Applications, and on a Mac with DeepSeek Harness.app
// installed dsh's CLI was never missing (#1823, mdwsk88). Every reader of a
// Mac's installed apps takes its folders from here.
func macAppFolders(home string) []string {
	return append(proc.SystemDirs("/Applications"), filepath.Join(home, "Applications"))
}

// windowsStorePackages lists the package full names registered for the
// current user on Windows, nil elsewhere; a var so tests never read the
// machine's own registry.
var windowsStorePackages = installedWindowsStorePackages

var (
	plistIDRe      = regexp.MustCompile(`<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`)
	plistVersionRe = regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`)
)

// AppVersion is the version of the agent's desktop app installed here, ""
// for an agent without one magpie knows, one not installed, or an Info.plist
// that doesn't say (a binary one isn't read).
func (a *Agent) AppVersion() string {
	app, ok := desktopApps[a.ID]
	if !ok || a.WSL != "" {
		return ""
	}
	for _, dir := range appFolders() {
		for _, name := range app.names {
			b, err := os.ReadFile(filepath.Join(dir, name, "Contents", "Info.plist"))
			if err != nil {
				continue
			}
			id, v := plistIDRe.FindSubmatch(b), plistVersionRe.FindSubmatch(b)
			if id == nil || v == nil || strings.TrimSpace(string(id[1])) != app.id {
				continue
			}
			return strings.TrimSpace(string(v[1]))
		}
	}
	if app.store != "" {
		return storeAppVersion(windowsStorePackages(), app.store, app.publisher)
	}
	return ""
}

// storeAppVersion finds the newest version of an app among Microsoft Store
// package full names (name_version_architecture_resourceid_publisher). Only
// the main package counts, the one with no resource id: the AppX registry
// keeps a resource package (…_neutral_split.scale-125_…) of a version the
// main package has moved past, so one is no sign the app is installed.
func storeAppVersion(packages []string, name, publisher string) string {
	prefix, suffix := name+"_", "_"+publisher
	var newest [4]uint64
	found := ""
	for _, pkg := range packages {
		if !strings.HasPrefix(pkg, prefix) || !strings.HasSuffix(pkg, suffix) {
			continue
		}
		fields := strings.Split(strings.TrimSuffix(strings.TrimPrefix(pkg, prefix), suffix), "_")
		if len(fields) != 3 || fields[2] != "" {
			continue
		}
		parts := strings.Split(fields[0], ".")
		if len(parts) != len(newest) {
			continue
		}
		var version [4]uint64
		valid := true
		for i, part := range parts {
			var err error
			version[i], err = strconv.ParseUint(part, 10, 16)
			if err != nil {
				valid = false
				break
			}
		}
		if valid && (found == "" || slices.Compare(version[:], newest[:]) > 0) {
			newest, found = version, fields[0]
		}
	}
	return found
}

// HasApp reports whether the agent's desktop app is installed here: its
// bundle on macOS (AppVersion), its executable on Windows.
func (a *Agent) HasApp() bool {
	if a.AppVersion() != "" {
		return true
	}
	app, ok := desktopApps[a.ID]
	if !ok || a.WSL != "" || app.win == "" {
		return false
	}
	for _, dir := range appFolders() {
		if isFile(filepath.Join(dir, app.win)) {
			return true
		}
	}
	return false
}
