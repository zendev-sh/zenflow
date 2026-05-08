package main

// cmd_validate.go - `zenflow validate <file>` subcommand. Loads the
// workflow YAML through the public `zenflow.LoadWorkflow` parser
// (which runs the parser + Go-side validator); prints "✓ Valid" on
// success and exits with code 2 (validation error) on failure.

import (
	"fmt"

	"github.com/zendev-sh/zenflow"
)

func cmdValidate() {
	a := osArgs()
	if len(a) < 3 {
		fmt.Fprintln(stderr, "usage: zenflow validate <file>")
		exit(3)
		return
	}
	if argsContainHelp(a[2:]) {
		usage(stdout)
		return
	}
	_, err := zenflow.LoadWorkflow(a[2])
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		exit(2)
		return
	}
	fmt.Fprintln(stdout, "✓ Valid")
}
