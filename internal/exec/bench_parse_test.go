package exec

// bench_parse_test.go - BenchmarkParseWorkflow
// Measures ParseWorkflow throughput on three YAML sizes:
// - small: 1-step minimal workflow
// - medium: 3-step simple chain (realistic short CI pipeline)
// - large: multi-agent workflow with every field populated (mirrors
// the full-featured testcase fixture)
// YAML is inlined as constants so the benchmark has no disk I/O; we
// call b.ResetTimer after any per-run setup to exclude it.
// Run:
//	go test -bench=BenchmarkParseWorkflow -benchtime=1x -run=XXX ./zenflow/

import "testing"

const benchParseSmallYAML = `
name: hello-world
steps:
  - id: greet
    instructions: "Say hello"
`

const benchParseMediumYAML = `
name: build-pipeline
steps:
  - id: compile
    instructions: "Compile the Go project"
  - id: test
    instructions: "Run unit tests"
    dependsOn: [compile]
  - id: deploy
    instructions: "Deploy to staging"
    dependsOn: [test]
`

// benchParseLargeYAML is a representative large workflow with agents,
// options, retries, model overrides, and many steps - roughly mirrors
// the full-featured.yaml testcase.
const benchParseLargeYAML = `
name: full-featured-workflow
description: "Demonstrates every field in the zenflow schema"
version: 1
agents:
  architect:
    description: "Designs system architecture"
    model: "openai/gpt-4o"
    tools: ["read_file", "write_file", "search"]
    disallowedTools: ["execute_command"]
    maxTurns: 10
    temperature: 0.7
    topP: 0.9
  coder:
    description: "Implements code changes"
    prompt: "You are a senior Go developer."
    model: "openai/gpt-4o"
    tools: ["read_file", "write_file", "search", "execute_command"]
    maxTurns: 20
  reviewer:
    description: "Reviews code for quality"
    model: "openai/gpt-4o"
    tools: ["read_file", "search"]
    temperature: 0.3
options:
  maxConcurrency: 4
  timeout: 30m
steps:
  - id: plan
    agent: architect
    instructions: "Plan the system design"
  - id: implement-a
    agent: coder
    instructions: "Implement component A"
    dependsOn: [plan]
    timeout: 10m
    retries: 3
  - id: implement-b
    agent: coder
    instructions: "Implement component B"
    dependsOn: [plan]
    timeout: 10m
    retries: 3
  - id: review-a
    agent: reviewer
    instructions: "Review component A"
    dependsOn: [implement-a]
  - id: review-b
    agent: reviewer
    instructions: "Review component B"
    dependsOn: [implement-b]
  - id: integrate
    agent: coder
    instructions: "Integrate both components"
    dependsOn: [review-a, review-b]
  - id: final-review
    agent: reviewer
    instructions: "Final integration review"
    dependsOn: [integrate]
`

// BenchmarkParseWorkflow - YAML parse throughput at three payload sizes.
// Each sub-benchmark allocates a new Workflow per iteration (ParseWorkflow
// always returns a fresh pointer); b.ReportAllocs captures the per-call
// allocation budget.
func BenchmarkParseWorkflow(b *testing.B) {
	cases := []struct {
		name string
		yaml string
	}{
		{"small", benchParseSmallYAML},
		{"medium", benchParseMediumYAML},
		{"large", benchParseLargeYAML},
	}

	for _, tc := range cases {
		data := []byte(tc.yaml)
		b.Run(tc.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				wf, err := ParseWorkflow(data)
				if err != nil {
					b.Fatalf("ParseWorkflow: %v", err)
				}
				_ = wf
			}
		})
	}
}
