package exec

import "context"

// NopIsolation provides no environment isolation between steps.
// All steps run in the same working directory.
type NopIsolation struct{}

// Compile-time guard that NopIsolation satisfies StepIsolation.
var _ StepIsolation = (*NopIsolation)(nil)

// Setup implements StepIsolation. NopIsolation returns an empty workDir (no isolation applied).
func (n *NopIsolation) Setup(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

// Cleanup implements StepIsolation. NopIsolation is a no-op.
func (n *NopIsolation) Cleanup(_ context.Context, _, _ string) error {
	return nil
}
