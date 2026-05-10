//go:build example

// Loop Bidirectional - repeat-until loop with bidirectional messaging
// between coordinator and the per-iteration worker. Each iteration
// the worker step sends progress to the coordinator; the coordinator
// can address the worker by bare name or by the namespaced
// `loop-stages.<i>.worker` form. Both routes resolve to the active
// iteration's nested router via root-router delegation.
// Run from the zenflow/ directory:
//
//	export GEMINI_API_KEY=...
//	go run -tags example ./examples/loop-bidirectional/
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
	wf, err := zenflow.LoadWorkflow("spec/v1/examples/loop-bidirectional.yaml")
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
