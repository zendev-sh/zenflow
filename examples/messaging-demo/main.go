//go:build example

// Messaging Demo - agents communicate exclusively through the
// coordinator. Three rounds of question/answer between an "asker" and
// an "expert"; the coordinator forwards each message between the two,
// then narrates a closing summary. Demonstrates hub-and-spoke routing
// where peer agents never address each other directly.
// Run from the zenflow/ directory:
//
//	export GEMINI_API_KEY=...
//	go run -tags example ./examples/messaging-demo/
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
	wf, err := zenflow.LoadWorkflow("spec/v1/examples/messaging-demo.yaml")
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
