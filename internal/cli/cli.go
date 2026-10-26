// Package cli is engram's command line: the interface agents and Legato use.
package cli

import (
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

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

// Env is everything a command reads from its surroundings.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	Cwd    string
	Now    func() time.Time
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return usageError{fmt.Sprintf(format, args...)}
}

type app struct {
	env   Env
	store *store.Store
	where *place
}

// Run executes one engram command and returns its exit code.
func Run(args []string, env Env) int {
	a := &app{env: env, store: store.Open(homeDir(env.Getenv))}
	err := a.run(args)
	if err == nil {
		return exitOK
	}
	fmt.Fprintf(env.Stderr, "engram: %v\n", err)
	var usage usageError
	if errors.As(err, &usage) {
		return exitUsage
	}
	return exitError
}

// homeDir resolves the store root: ENGRAM_HOME, then the XDG data directory.
func homeDir(getenv func(string) string) string {
	if home := getenv("ENGRAM_HOME"); home != "" {
		return home
	}
	if data := getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "engram")
	}
	return filepath.Join(getenv("HOME"), ".local", "share", "engram")
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
	in, err := cmd.parse(rest)
	if errors.Is(err, flag.ErrHelp) {
		a.printf("%s", cmd.help())
		return nil
	}
	if err == nil {
		err = cmd.run(a, in)
	}
	// Whoever called it wrongly is told where the right way is written down.
	var usage usageError
	if errors.As(err, &usage) {
		return usagef("%s\nRun `engram help %s` for usage.", usage.msg, cmd.name)
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
