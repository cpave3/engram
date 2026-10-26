package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// flagDef declares one flag. The declaration drives both parsing and help, so
// the help can never describe a flag the command does not take.
type flagDef struct {
	name  string
	value string // placeholder shown in help, such as "<kind>"; empty for a switch
	def   string // default value
	help  string
}

// command is one thing engram can be asked to do, with everything an agent
// needs to read before using it.
type command struct {
	name     string // as typed after "engram": "remember", "thread open"
	args     string // positional arguments, as shown in the usage line
	summary  string // one line, for the overview
	detail   string // when to use it and what its choices mean
	flags    []flagDef
	examples []string
	prints   string // what it writes to standard output
	acts     bool   // acts as an identity, so it also takes --as
	run      func(*app, input) error
}

var asFlag = flagDef{name: "as", value: "<name>", help: "identity to act as; also accepted before the command (default: $ENGRAM_IDENTITY, then matching session)"}

func (c *command) allFlags() []flagDef {
	if !c.acts {
		return c.flags
	}
	return append(c.flags[:len(c.flags):len(c.flags)], asFlag)
}

// input is a command's parsed arguments.
type input struct {
	args     []string
	values   map[string]*string
	switches map[string]*bool
}

func (in input) str(name string) string {
	value, ok := in.values[name]
	if !ok {
		panic("engram: flag --" + name + " is not declared for this command")
	}
	return *value
}

func (in input) on(name string) bool {
	value, ok := in.switches[name]
	if !ok {
		panic("engram: switch --" + name + " is not declared for this command")
	}
	return *value
}

// num reads a flag that must be a whole number of at least min.
func (in input) num(name string, min int) (int, error) {
	n, err := strconv.Atoi(in.str(name))
	if err != nil || n < min {
		return 0, usagef("--%s must be a whole number of at least %d, not %q", name, min, in.str(name))
	}
	return n, nil
}

func (c *command) parse(args []string) (input, error) {
	fs := flag.NewFlagSet(c.name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	in := input{values: map[string]*string{}, switches: map[string]*bool{}}
	for _, f := range c.allFlags() {
		if f.value == "" {
			in.switches[f.name] = fs.Bool(f.name, false, "")
		} else {
			in.values[f.name] = fs.String(f.name, f.def, "")
		}
	}
	positional, err := interspersed(fs, args)
	in.args = positional
	return in, err
}

// interspersed accepts flags before, between, or after positional arguments,
// because agents write `engram remember "claim" --kind pitfall` as readily as
// the reverse. Everything after a bare -- is positional.
func interspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var tail []string
	for i, arg := range args {
		if arg == "--" {
			args, tail = args[:i], args[i+1:]
			break
		}
	}
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, err
			}
			return nil, usageError{err.Error()}
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return append(positional, tail...), nil
}

var commandIndex = func() map[string]*command {
	index := make(map[string]*command, len(commandList))
	for i := range commandList {
		index[commandList[i].name] = &commandList[i]
	}
	return index
}()

// Move identity flags ahead of command words into the command's flag parser.
// Stop at the command itself so positional text and bare -- keep their meaning.
func commandFlags(args []string) ([]string, error) {
	var words, flags []string
	for len(args) > 0 {
		arg := args[0]
		if arg == "--as" || arg == "-as" {
			if len(args) < 2 {
				return nil, usagef("--as needs an identity name; e.g. `engram --as ada boot`")
			}
			flags = append(flags, args[:2]...)
			args = args[2:]
			continue
		}
		if strings.HasPrefix(arg, "--as=") || strings.HasPrefix(arg, "-as=") {
			flags = append(flags, arg)
			args = args[1:]
			continue
		}
		words = append(words, arg)
		args = args[1:]
		if commandIndex[strings.Join(words, " ")] != nil || len(subcommands(arg)) == 0 {
			break
		}
	}
	return append(append(words, flags...), args...), nil
}

// lookup finds the command named by the leading one or two arguments.
func lookup(args []string) (*command, []string, error) {
	if len(args) >= 2 {
		if cmd := commandIndex[args[0]+" "+args[1]]; cmd != nil {
			return cmd, args[2:], nil
		}
	}
	if cmd := commandIndex[args[0]]; cmd != nil {
		return cmd, args[1:], nil
	}
	if subs := subcommands(args[0]); len(subs) > 0 {
		return nil, nil, usagef("%s needs a subcommand: %s\nRun `engram help %s` for usage.", args[0], strings.Join(subs, ", "), args[0])
	}
	return nil, nil, usagef("unknown command %q\nRun `engram help` for the list of commands.", args[0])
}

// subcommands lists the second words of the commands that start with group.
func subcommands(group string) []string {
	var subs []string
	for _, cmd := range commandList {
		if rest, ok := strings.CutPrefix(cmd.name, group+" "); ok {
			subs = append(subs, rest)
		}
	}
	return subs
}

// help renders everything the command declares.
func (c *command) help() string {
	var b strings.Builder
	fmt.Fprintf(&b, "engram %s — %s\n\nUsage:\n  %s\n", c.name, c.summary, c.usage())
	if c.detail != "" {
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(c.detail))
	}
	if flags := c.allFlags(); len(flags) > 0 {
		b.WriteString("\nFlags:\n")
		width := 0
		for _, f := range flags {
			width = max(width, len(f.syntax()))
		}
		for _, f := range flags {
			help := f.help
			if f.def != "" {
				help += " (default: " + f.def + ")"
			}
			fmt.Fprintf(&b, "  %-*s  %s\n", width, f.syntax(), help)
		}
	}
	if len(c.examples) > 0 {
		b.WriteString("\nExamples:\n")
		for i, example := range c.examples {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(indent(example) + "\n")
		}
	}
	fmt.Fprintf(&b, "\nPrints:\n%s\n", indent(strings.TrimSpace(c.prints)))
	return b.String()
}

func (c *command) usage() string {
	usage := "engram " + c.name
	if c.args != "" {
		usage += " " + c.args
	}
	if len(c.allFlags()) > 0 {
		usage += " [flags]"
	}
	return usage
}

func (f flagDef) syntax() string {
	if f.value == "" {
		return "--" + f.name
	}
	return "--" + f.name + " " + f.value
}
