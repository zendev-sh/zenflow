package main

// cmd_plan.go - `zenflow plan <file>` subcommand. Loads the workflow
// and prints the DAG layout via `dag.Render`. Workflow load failure
// exits with code 2 (validation error); rendering itself never fails.

import (
	"fmt"

	"github.com/zendev-sh/zenflow"
	"github.com/zendev-sh/zenflow/cmd/zenflow/dag"
)

func cmdPlan() {
	a := osArgs()
	if len(a) < 3 {
		fmt.Fprintln(stderr, "usage: zenflow plan <file>")
		exit(3)
		return
	}
	if argsContainHelp(a[2:]) {
		usage(stdout)
		return
	}
	wf, err := zenflow.LoadWorkflow(a[2])
	if err != nil {
		fmt.Fprintf(stderr, "✗ %v\n", err)
		exit(2)
		return
	}
	fmt.Fprint(stdout, dag.Render(wf))
}
