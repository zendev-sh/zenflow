package exec

import (
	"testing"
)

func TestNopIsolation_Setup(t *testing.T) {
	iso := &NopIsolation{}
	workDir, err := iso.Setup(t.Context(), "run-1", "step-1")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if workDir != "" {
		t.Errorf("workDir = %q, want empty (current dir)", workDir)
	}
}

func TestNopIsolation_Cleanup(t *testing.T) {
	iso := &NopIsolation{}
	if err := iso.Cleanup(t.Context(), "run-1", "step-1"); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
}

func TestNopIsolation_ImplementsInterface(t *testing.T) {
	var _ StepIsolation = &NopIsolation{}
}
