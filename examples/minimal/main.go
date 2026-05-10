//go:build example

// Minimal - smallest valid zenflow workflow. One step, no agents
// section, no coordinator. The step's instructions are sent to the
// default LLM as a one-shot prompt.
// Run from the zenflow/ directory:
//
//	export GEMINI_API_KEY=...
//	go run -tags example ./examples/minimal/
//
// To use a different provider, swap the google.Chat(...) call below
// for bedrock.Chat(...) or azure.Chat(...) and set the corresponding
// env var (AWS_ACCESS_KEY_ID / AZURE_OPENAI_API_KEY).
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
	wf, err := zenflow.LoadWorkflow("spec/v1/examples/minimal.yaml")
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
