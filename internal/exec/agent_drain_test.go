package exec

// retired the slowCoordinator regression test
// (TestZFB1_SuccessDrain_WaitsForSlowCoordinator). The legacy bug it
// covered - successDrain bailing after 2s while a real LLM
// coordinator's per-step narration was still in flight - could only
// occur because notifyCoordinator made an LLM call inline. removes
// that call entirely; notifyCoordinator now only performs a synchronous
// Mailbox.Append, which completes in microseconds. The successDrain
// path remains, but its slow-coordinator failure mode is structurally
// impossible after. The successDrain wait still applies to slow
// storage backends; that timing is covered by the storage_file_test.go
// fsync tests.
