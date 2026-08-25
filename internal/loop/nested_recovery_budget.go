package loop

import (
	"fmt"
	"time"
)

func validateNestedRecoveryBudget(run Run, nextGeneration int, at time.Time) error {
	return ValidateNestedRecoveryBudget(run, nextGeneration, at)
}

// ValidateNestedRecoveryBudget performs the shared, non-mutating inherited-budget preflight.
func ValidateNestedRecoveryBudget(run Run, nextGeneration int, at time.Time) error {
	if run.PauseRequested || run.CancelRequested {
		return nestedRecoveryConflictError("run has a pending pause or cancellation")
	}
	if run.IterationCap > 0 && nextGeneration > run.IterationCap {
		return nestedRecoveryBudgetError("iteration cap is exhausted")
	}
	if run.BudgetTokens > 0 && run.TokensUsed >= int64(run.BudgetTokens) {
		return nestedRecoveryBudgetError("token budget is exhausted")
	}
	if run.BudgetWallSec > 0 && !run.StartedAt.IsZero() &&
		!at.Before(run.StartedAt.Add(time.Duration(run.BudgetWallSec)*time.Second)) {
		return nestedRecoveryBudgetError("wall-clock budget is exhausted")
	}
	return nil
}

func nestedRecoveryConflictError(detail string) error {
	return &ReasonError{
		Code: ReasonCodeNestedRecoveryConflict,
		Err:  fmt.Errorf("%w: %s", ErrNestedRecoveryConflict, detail),
	}
}

func nestedRecoveryBudgetError(detail string) error {
	return &ReasonError{
		Code: ReasonCodeNestedRecoveryBudgetExhausted,
		Err:  fmt.Errorf("%w: %s", ErrNestedRecoveryBudgetExhausted, detail),
	}
}
