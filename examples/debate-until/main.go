//go:build example

// Debate Until - multi-round debate with judge-controlled termination.
// Each round: pro argues, con counters (sequential within round); the
// judge evaluates after each round and stops the loop when one side is
// clearly winning or arguments are exhausted, capped at five rounds.
// Demonstrates the repeat-until loop primitive plus a termination
// agent.
// Run from the zenflow/ directory:
//	export GEMINI_API_KEY=...
//	go run -tags example ./examples/debate-until/
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/zendev-sh/goai/provider/google"
	"github.com/zendev-sh/zenflow"
)

func main() {
	wf, err := zenflow.LoadWorkflow("spec/v1/examples/debate-until.yaml")
	if err != nil {
		log.Fatal("load: ", err)
	}

	llm := google.Chat("gemini-2.0-flash", google.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
	orch := zenflow.New(
		zenflow.WithModel(llm),
		zenflow.WithCoordinator(zenflow.NewDefaultCoordRunner(llm)),
	)
	defer orch.Close()

	result, err := orch.RunFlow(context.Background(), wf)
	if err != nil {
		log.Fatal("run: ", err)
	}

	fmt.Printf("run %q: status=%q duration=%s steps=%d\n",
		result.RunID, result.Status, result.Duration, len(result.Steps))
	if result.Summary != "" {
		fmt.Println()
		fmt.Println(result.Summary)
	}
}
