//go:build example

// Loop ForEach - parallel iteration over a dynamic array drawn from a
// previous step's output. The first step lists microservices, then a
// forEach loop deploys each one in parallel.
// Run from the zenflow/ directory:
//
//	export GEMINI_API_KEY=...
//	go run -tags example ./examples/loop-foreach/
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
	wf, err := zenflow.LoadWorkflow("spec/v1/examples/loop-foreach.yaml")
	if err != nil {
		log.Fatal("load: ", err)
	}

	llm := google.Chat("gemini-2.0-flash", google.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
	orch := zenflow.New(zenflow.WithModel(llm))
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
