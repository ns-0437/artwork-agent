// Package agent holds the bounded agent loop's provider adapter. The
// provider is single and replaceable (CLAUDE.md point 14): it chooses ONE
// next action from findings Go has already computed deterministically - it
// never recomputes a measurement or overrides eligibility (CLAUDE.md point
// 1). Swapping providers means implementing Provider again; nothing else in
// the codebase depends on which one is active.
package agent

import "context"

// FindingSummary is a minimal, provider-agnostic view of one check result -
// deliberately NOT the full store.Finding, so a provider implementation
// can't reach back into anything beyond what it's given.
type FindingSummary struct {
	CheckName string `json:"check_name"`
	Result    string `json:"result"`
	Evidence  string `json:"evidence"`
}

type DecisionInput struct {
	OrderID        string
	ProductType    string
	DeclaredWidth  float64
	DeclaredHeight float64
	DeclaredUnit   string
	Intent         string
	Findings       []FindingSummary
}

// Decision.Action is exactly one of: "ask_clarification", "request_repair",
// "escalate" - enforced by the provider implementation before it's ever
// returned (see groq_adapter.go).
type Decision struct {
	Action   string
	Question string // set only when Action == "ask_clarification"

	// TokenUsage is the provider's own reported token accounting for this
	// call, when it reports one (zero value if not) - logged to
	// tool_events purely for cost observability (evals/scripts/run_eval.py
	// aggregates it for the agent-vs-scripted cost comparison). Never used
	// for anything budget/decision-relevant - that's agent_tool_calls_used/
	// agent_retries_used (point 6), which are enforced regardless of
	// whether a provider reports usage at all.
	TokenUsage TokenUsage
}

type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

const (
	ActionAskClarification = "ask_clarification"
	ActionRequestRepair    = "request_repair"
	ActionEscalate         = "escalate"
)

type Provider interface {
	Decide(ctx context.Context, in DecisionInput) (Decision, error)
}
