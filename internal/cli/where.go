package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// Version is set at release build time:
//
//	go build -ldflags "-X github.com/cpave3/engram/internal/cli.Version=v0.2.0"
//
// When it is empty, the module version from the build info is used, which
// `go install github.com/cpave3/engram/cmd/engram@<version>` fills in.
var Version string

func version() string {
	if Version != "" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

func (a *app) version(in input) error {
	if len(in.args) > 0 {
		return usagef("version takes no arguments")
	}
	if in.on("json") {
		return a.writeJSON(map[string]string{"version": version()})
	}
	a.printf("engram %s\n", version())
	return nil
}

// whereReport is what `engram where --json` prints. Its field names are a
// contract with programs that launch engram.
type whereReport struct {
	Binary  string      `json:"binary"`
	Version string      `json:"version"`
	Store   whereStore  `json:"store"`
	Place   *wherePlace `json:"place,omitempty"`
}

type whereStore struct {
	Root   string `json:"root"`
	Source string `json:"source"`
	Exists bool   `json:"exists"`
}

type wherePlace struct {
	Dir     string   `json:"dir"`
	Primary string   `json:"primary"`
	Scopes  []string `json:"scopes"`
}

// where reports where engram is and where it would keep and find things. It
// needs no identity and never starts a session.
func (a *app) whereCmd(in input) error {
	if len(in.args) > 0 {
		return usagef("where takes no arguments; pass a directory with --dir <path>")
	}
	executable := a.env.Executable
	if executable == nil {
		executable = os.Executable
	}
	binary, err := executable()
	if err != nil {
		return fmt.Errorf("finding the engram binary: %w", err)
	}
	exists, err := a.storeExists()
	if err != nil {
		return err
	}
	report := whereReport{
		Binary:  binary,
		Version: version(),
		Store:   whereStore{Root: a.abs(a.home.root), Source: a.home.source, Exists: exists},
	}
	if dir := in.str("dir"); dir != "" {
		dir = a.abs(dir)
		info, err := os.Stat(dir)
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("--dir %s: no such directory", dir)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("--dir %s: not a directory", dir)
		}
		p := placeOf(dir)
		report.Place = &wherePlace{Dir: dir, Primary: p.primary, Scopes: p.scopes}
	}
	if in.on("json") {
		return a.writeJSON(report)
	}
	state := "exists"
	if !exists {
		state = "missing"
	}
	a.printf("binary   %s\nversion  %s\nstore    %s (from %s; %s)\n", report.Binary, report.Version, report.Store.Root, report.Store.Source, state)
	if p := report.Place; p != nil {
		a.printf("dir      %s\nprimary  %s\nscopes   %s\n", p.Dir, p.Primary, strings.Join(p.Scopes, ", "))
	}
	return nil
}

// abs resolves path against the directory engram runs in.
func (a *app) abs(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(a.env.Cwd, path)
}
