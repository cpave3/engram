package main

import (
	"fmt"
	"os"
	"time"

	"github.com/cpave3/engram/internal/cli"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "engram: working directory: %v\n", err)
		os.Exit(1)
	}
	os.Exit(cli.Run(os.Args[1:], cli.Env{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Getenv: os.Getenv,
		Cwd:    cwd,
		Now:    time.Now,
	}))
}
