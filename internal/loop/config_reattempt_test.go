package loop

import (
	"testing"

	"github.com/compozy/compozy/internal/loop/dsl"
)

func TestValidateEffectiveConfigShouldAcceptHaltReattemptStrategy(t *testing.T) {
	t.Parallel()

	err := validateEffectiveConfig(EffectiveConfig{
		ReattemptStrategy: ReattemptHalt,
		EnabledChecks:     []byte(`{}`),
		BudgetOnExceeded:  dsl.BudgetExceededHalt,
		Environment:       dsl.EnvironmentSpec{Mode: dsl.EnvironmentRoot},
		Lifecycle:         DefaultLifecycleConfig(),
	})
	if err != nil {
		t.Fatalf("validateEffectiveConfig(halt) error = %v", err)
	}
}
