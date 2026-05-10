//go:build example

// Product Launch - parallel pricing, marketing, and legal review
// coordinated through messaging. Market research completes first,
// then the three teams work in parallel; the coordinator forwards
// pricing decisions to the marketing copy writer and legal
// constraints to both pricing and marketing.
// Run from the zenflow/ directory:
//
//	export GEMINI_API_KEY=...
//	go run -tags example ./examples/product-launch/
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
	wf, err := zenflow.LoadWorkflow("spec/v1/examples/product-launch.yaml")
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
