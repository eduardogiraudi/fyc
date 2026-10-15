package providers

import (
	"testing"

	"fuckyourcli/reasoning"
)

// TestAnthropicMaxTokensKeepsRoomForBudget pins the budget_tokens < max_tokens
// requirement: a cap at or below the resolved thinking budget is raised above it
// so the Messages API does not answer with a 400.
func TestAnthropicMaxTokensKeepsRoomForBudget(t *testing.T) {
	p := reasoning.Profile{
		TogglePath:         "thinking.type",
		ToggleEnabledValue: "enabled",
		BudgetPath:         "thinking.budget_tokens",
		BudgetMin:          1024,
		BudgetFromEffort:   map[string]int{"high": 16000},
		ToggleFromControl:  true,
	}
	plan, err := reasoning.Translate(reasoning.Intent{Mode: reasoning.ModeEffort, Effort: "high"}, p)
	if err != nil {
		t.Fatal(err)
	}
	if got := anthropicPlanBudget(plan); got != 16000 {
		t.Fatalf("plan budget = %d, want 16000", got)
	}

	// A generous cap is left unchanged.
	if got := anthropicMaxTokens(65536, plan); got != 65536 {
		t.Fatalf("max_tokens = %d, want 65536", got)
	}
	// A cap at or below the budget is raised above it.
	if got := anthropicMaxTokens(8000, plan); got != 20096 {
		t.Fatalf("max_tokens = %d, want 20096 (budget 16000 + 4096 room)", got)
	}
	if got := anthropicMaxTokens(16000, plan); got != 24000 {
		t.Fatalf("max_tokens = %d, want 24000 (budget 16000 + 8000 room)", got)
	}
	// An unset cap must still leave room for the answer past the budget.
	if got := anthropicMaxTokens(0, plan); got != 20096 {
		t.Fatalf("max_tokens = %d, want 20096 for an unset cap with a budget", got)
	}

	// No budget in the plan: the cap is untouched.
	if got := anthropicMaxTokens(4000, reasoning.Plan{}); got != 4000 {
		t.Fatalf("max_tokens = %d, want 4000", got)
	}
}
