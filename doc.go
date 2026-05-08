// Package zenflow is a declarative multi-agent workflow engine for Go.
// A zenflow workflow is a YAML DAG of steps. Each step has an agent
// (an LLM-backed conversational role), instructions, optional dependencies,
// and an optional retry/timeout/isolation policy. The engine schedules
// steps respecting their dependencies, runs them concurrently when safe,
// and threads inter-step messages through a coordinator.
// Three execution modes share one engine:
// - [Orchestrator.RunFlow] runs a fully-declared YAML DAG.
// - [Orchestrator.RunGoal] asks a coordinator LLM to plan a workflow
// from a goal string, then runs it.
// - [Orchestrator.RunAgent] runs a single agent loop with no DAG.
// The CLI binary at cmd/zenflow is a thin wrapper around the same
// Orchestrator. Embedders use the library form directly:
//	import "github.com/zendev-sh/zenflow"
//	func main {
// orch := zenflow.New(zenflow.WithModelResolver(modelResolver))
// defer orch.Close
// wf, err := zenflow.LoadWorkflow("workflow.yaml")
// if err != nil { panic(err) }
// res, err := orch.RunFlow(ctx, wf)
// // ...
//	}
// # Coordinator and messaging
// A coordinator is itself an [AgentRunner] running in parallel with the
// step DAG. It owns four default tools: forward_to_agent (coord ->
// step's mailbox), send_message (step -> coord upstream), narrate (emit
// a progress event), and finalize (terminate the run with a summary).
// Peer agents never address each other directly - every inter-step
// message flows through the coordinator (hub-and-spoke).
// # Race-safe delivery
// Every send goes through the [DeliveryEngine] (Mailbox + Wake pair).
// The Mailbox is the per-step inbox; the Wake is a 1-buffered channel
// that signals "new mail." A drop returns a typed [DropReason] - there
// is no silent loss, no out-of-order delivery, and no leaked goroutines.
// # Resume from transcript
// A long step can be resumed across process boundaries via the
// [TranscriptStore] interface. The executor records the system prompt,
// model ID, and message history; resume calls [Executor.ResumeStep] to
// spawn a fresh runner that continues from the last appended turn.
// # Provider routing
// zenflow does no LLM I/O itself. All provider work goes through goai
// (https://goai.sh). Any model goai supports - Google Gemini, AWS
// Bedrock, Azure (DeepSeek / Anthropic / GPT chat / GPT responses),
// Vertex, and others - works as a zenflow agent backend. See
// goai.ProviderRouter and the [WithModel] option.
// # Stability
// Pre-1.0. The exported surface (Orchestrator, RunFlow/RunGoal/RunAgent,
// the With* options, Workflow / Step / StepResult / WorkflowResult, the
// MailboxStore / TranscriptStore / Storage / StepIsolation interfaces,
// and the YAML schema in spec/v1/) is intended to settle at v0.1.0 but
// may still change.
// See docs/architecture, docs/concepts, and docs/yaml at zenflow.sh for
// the full reference. The CLI's own help is `zenflow --help`.
package zenflow
