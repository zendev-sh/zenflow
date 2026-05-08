package zenflow_test

import (
	"testing"

	"github.com/zendev-sh/zenflow"
)

// Compile-time assertions: the documented backward-compat aliases must
// remain assignable to the underlying renamed types. A future accidental
// rename inside internal/* will fail this file's compile and surface
// the breakage to maintainers before release.
// Every documented backward-compat alias has a pin below; a rename
// inside internal/* that drops one of these will fail to compile here.
var (
	_ zenflow.RouterMessage     = zenflow.RouterMessage{}
	_ zenflow.RouterMessageType = zenflow.RouterMessageInfo
	_ zenflow.RouterMessageType = zenflow.RouterMessageCancel
	_ zenflow.RouterMessageType = zenflow.RouterMessageContextUpdate
	_ zenflow.RouterMessageType = zenflow.RouterMessageResumeReply

	_ *zenflow.MessageRouter = (*zenflow.MessageRouter)(nil)

	// AgentRunnerOption alias points at the renamed RunnerOption type.
	_ zenflow.AgentRunnerOption = zenflow.AgentRunnerOption(nil)

	// Storage composition + narrow role interfaces.
	_ zenflow.Storage           = (*zenflow.MemoryStorage)(nil)
	_ zenflow.Storage           = (*zenflow.FileStorage)(nil)
	_ zenflow.RunStore          = (*zenflow.MemoryStorage)(nil)
	_ zenflow.StepResultStore   = (*zenflow.MemoryStorage)(nil)
	_ zenflow.SharedMemoryStore = (*zenflow.MemoryStorage)(nil)
)

// TestNewMessageRouterReturnsExpectedType pins the constructor's return
// type at runtime in addition to the compile-time aliases above. If
// router.NewRouter ever changes return type, this test fails to compile.
func TestNewMessageRouterReturnsExpectedType(t *testing.T) {
	var _ *zenflow.MessageRouter = zenflow.NewMessageRouter()
}

// TestNewMemoryStorageSatisfiesAllStores pins that the in-memory storage
// implementation satisfies the composed Storage interface AND each of
// the narrow role interfaces (RunStore, StepResultStore,
// SharedMemoryStore).
func TestNewMemoryStorageSatisfiesAllStores(t *testing.T) {
	s := zenflow.NewMemoryStorage()
	var _ zenflow.Storage = s
	var _ zenflow.RunStore = s
	var _ zenflow.StepResultStore = s
	var _ zenflow.SharedMemoryStore = s
}
