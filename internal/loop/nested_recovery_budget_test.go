package loop

import (
	"errors"
	"testing"
	"time"
)

func TestNestedRecoveryBudgetShouldRejectReactivationWithoutResettingOriginalAccounting(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.August, 25, 15, 0, 0, 0, time.UTC)
	base := Run{
		ID: "child", Status: StatusFailed, Generation: 2,
		IterationCap: 4, BudgetTokens: 100, TokensUsed: 40,
		BudgetWallSec: 600, StartedAt: now.Add(-time.Minute),
	}
	for _, test := range []struct {
		name   string
		mutate func(*Run)
		want   error
	}{
		{name: "Should accept remaining inherited budgets", want: nil},
		{name: "Should reject the next generation beyond the original iteration cap", mutate: func(run *Run) {
			run.IterationCap = run.Generation
		}, want: ErrNestedRecoveryBudgetExhausted},
		{name: "Should reject accumulated tokens at the original ceiling", mutate: func(run *Run) {
			run.TokensUsed = int64(run.BudgetTokens)
		}, want: ErrNestedRecoveryBudgetExhausted},
		{name: "Should reject the original wall clock deadline", mutate: func(run *Run) {
			run.StartedAt = now.Add(-time.Duration(run.BudgetWallSec) * time.Second)
		}, want: ErrNestedRecoveryBudgetExhausted},
		{name: "Should reject a pending pause", mutate: func(run *Run) {
			run.PauseRequested = true
		}, want: ErrNestedRecoveryConflict},
		{name: "Should reject a pending cancellation", mutate: func(run *Run) {
			run.CancelRequested = true
		}, want: ErrNestedRecoveryConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := base
			if test.mutate != nil {
				test.mutate(&run)
			}
			beforeTokens, beforeStartedAt := run.TokensUsed, run.StartedAt
			err := validateNestedRecoveryBudget(run, run.Generation+1, now)
			if !errors.Is(err, test.want) {
				t.Fatalf("validateNestedRecoveryBudget() error = %v, want %v", err, test.want)
			}
			if run.TokensUsed != beforeTokens || run.StartedAt != beforeStartedAt {
				t.Fatalf("budget preflight mutated accounting: %#v", run)
			}
		})
	}
}
