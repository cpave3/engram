// Package cli is engram's command line: the interface agents and Legato use.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cpave3/engram/internal/store"
)

// Exit statuses. A program can tell these failures apart without reading the
// message; with --json the same failure is also printed as an error code.
const (
	exitOK              = 0
	exitError           = 1
	exitUsage           = 2
	exitUnknownIdentity = 3
	exitStoreMissing    = 4
	exitIdentityExists  = 5
)

// Error codes printed with --json, one per exit status and a few more.
const (
	codeError           = "error"
	codeUsage           = "usage"
	codeNoIdentity      = "no_identity"
	codeNoSession       = "no_session"
	codeUnknownIdentity = "unknown_identity"
	codeStoreMissing    = "store_missing"
	codeIdentityExists  = "identity_exists"
)

// Env is everything a command reads from its surroundings.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	Cwd    string
	Now    func() time.Time
	// Executable is the path of the running binary; nil means os.Executable.
	Executable func() (string, error)
}

type usageError struct {
	msg  string
	code string // codeUsage unless set
}

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return usageError{msg: fmt.Sprintf(format, args...)}
}

// codedError is a failure a program must be able to tell apart from others.
type codedError struct {
	code string
	exit int
	msg  string
}

func (e codedError) Error() string { return e.msg }

func unknownIdentityf(format string, args ...any) error {
	return codedError{codeUnknownIdentity, exitUnknownIdentity, fmt.Sprintf(format, args...)}
}

type app struct {
	env   Env
	store *store.Store
	home  storeHome
	where *place
	// jsonOut is set once a command was parsed with --json, so a failure is
	// also printed as JSON on standard output.
	jsonOut bool
}

// Run executes one engram command and returns its exit code.
func Run(args []string, env Env) int {
	home := homeDir(env.Getenv)
	a := &app{env: env, store: store.Open(home.root), home: home}
	err := a.run(args)
	if err == nil {
		return exitOK
	}
	fmt.Fprintf(env.Stderr, "engram: %v\n", err)
	code, exit := classify(err)
	if a.jsonOut {
		a.writeJSON(map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
	}
	return exit
}

// classify gives a failure its --json error code and exit status.
func classify(err error) (code string, exit int) {
	var coded codedError
	if errors.As(err, &coded) {
		return coded.code, coded.exit
	}
	var usage usageError
	if errors.As(err, &usage) {
		if usage.code != "" {
			return usage.code, exitUsage
		}
		return codeUsage, exitUsage
	}
	return codeError, exitError
}

// storeHome is the store root and what chose it.
type storeHome struct {
	root   string
	source string // ENGRAM_HOME, XDG_DATA_HOME or HOME
}

// homeDir resolves the store root: ENGRAM_HOME, then the XDG data directory,
// then ~/.local/share/engram.
func homeDir(getenv func(string) string) storeHome {
	if home := getenv("ENGRAM_HOME"); home != "" {
		return storeHome{home, "ENGRAM_HOME"}
	}
	if data := getenv("XDG_DATA_HOME"); data != "" {
		return storeHome{filepath.Join(data, "engram"), "XDG_DATA_HOME"}
	}
	return storeHome{filepath.Join(getenv("HOME"), ".local", "share", "engram"), "HOME"}
}

// storeExists reports whether the store root is a directory. A missing store
// is told apart from an identity missing from a store that exists.
func (a *app) storeExists() (bool, error) {
	info, err := os.Stat(a.home.root)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

// requireStore fails with store_missing when there is no store to act in.
func (a *app) requireStore() error {
	ok, err := a.storeExists()
	if err != nil {
		return err
	}
	if !ok {
		return codedError{codeStoreMissing, exitStoreMissing, fmt.Sprintf(
			"no engram store at %s (from %s): issue an identity with `engram identity new <name>`, or point ENGRAM_HOME at the store", a.home.root, a.home.source)}
	}
	return nil
}

func (a *app) writeJSON(v any) error {
	enc := json.NewEncoder(a.env.Stdout)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func (a *app) run(args []string) error {
	args, err := commandFlags(args)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		a.printf("%s", overview())
		return nil
	}
	if args[0] == "help" {
		return a.help(args[1:])
	}
	cmd, rest, err := lookup(args)
	if err != nil {
		return err
	}
	if _, ok := cmd.flagNamed("json"); ok {
		// Known before parsing, so a malformed flag is also reported as JSON.
		a.jsonOut = wantsJSON(rest)
	}
	in, err := cmd.parse(rest)
	if errors.Is(err, flag.ErrHelp) {
		a.printf("%s", cmd.help())
		return nil
	}
	if err == nil {
		if _, ok := in.switches["json"]; ok {
			a.jsonOut = in.on("json")
		}
		err = cmd.run(a, in)
	}
	// Whoever called it wrongly is told where the right way is written down.
	var usage usageError
	if errors.As(err, &usage) {
		usage.msg = fmt.Sprintf("%s\nRun `engram help %s` for usage.", usage.msg, cmd.name)
		return usage
	}
	return err
}

func (a *app) printf(format string, args ...any) {
	fmt.Fprintf(a.env.Stdout, format, args...)
}

// text returns a command's free-text input: its positional words, or the
// contents of --file (a path, or - for standard input).
func (a *app) text(positional []string, file, what string) (string, error) {
	if file != "" && len(positional) > 0 {
		return "", usagef("pass the %s as an argument or with --file, not both", what)
	}
	var raw string
	switch {
	case file == "-":
		data, err := io.ReadAll(a.env.Stdin)
		if err != nil {
			return "", err
		}
		raw = string(data)
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		raw = string(data)
	default:
		raw = strings.Join(positional, " ")
	}
	return strings.TrimSpace(raw), nil
}

// required is text that must not be empty.
func (a *app) required(positional []string, file, what string) (string, error) {
	text, err := a.text(positional, file, what)
	if err == nil && text == "" {
		return "", usagef("missing %s: pass it as an argument, or with --file <path|-> to read a file or standard input", what)
	}
	return text, err
}

// wantsJSON reports whether args turn on --json, before a bare --.
func wantsJSON(args []string) bool {
	on := false
	for _, arg := range args {
		switch arg {
		case "--":
			return on
		case "--json", "-json", "--json=true", "-json=true":
			on = true
		case "--json=false", "-json=false":
			on = false
		}
	}
	return on
}
