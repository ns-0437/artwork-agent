package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// GroqAdapter calls Groq's OpenAI-compatible chat completions API
// (https://api.groq.com/openai/v1/chat/completions) with the decision
// forced through a single tool call, so the response is always
// structured - never free text to parse and hope about.
//
// Groq (a fast inference host for open-weight models) is NOT xAI's Grok,
// which is what the brief's optional Day 5 comparison actually refers to.
// It's used here as a practical stand-in provider for the bounded loop
// while Anthropic account credits were unavailable - CLAUDE.md documents
// this distinction explicitly so it's never misrepresented as "Grok" or as
// the brief's intended Claude-first choice.
type GroqAdapter struct {
	apiKey  string
	model   string
	http    *http.Client
	baseURL string
}

func NewGroqAdapter(apiKey, model string) *GroqAdapter {
	if model == "" {
		model = "openai/gpt-oss-20b"
	}
	return &GroqAdapter{
		apiKey: apiKey,
		model:  model,
		// Deliberately shorter than worker.agentDecisionDeadline (20s),
		// which bounds the WHOLE retry sequence, not one call - a single
		// attempt hitting this timeout must still leave room for at least
		// one retry within that overall deadline, not consume all of it.
		http:    &http.Client{Timeout: 12 * time.Second},
		baseURL: "https://api.groq.com/openai/v1/chat/completions",
	}
}

// The prompt deliberately narrows ask_clarification to the ONE clarification
// v1 actually supports (trim confirmation) rather than describing
// NEEDS_INPUT generically - a low-resolution NEEDS_INPUT finding has no
// clarification that can resolve it, and the model should escalate that,
// not ask about trim. Go still independently verifies this before acting on
// the decision (see worker.runAgentDecision) - the prompt narrows what the
// model is LIKELY to choose, it isn't the enforcement.
const systemPrompt = `You coordinate an artwork exception case for a sticker order. You NEVER compute or override a measurement - the findings given to you are already deterministic and final, produced by rules code you cannot see or influence. Your only job is to choose exactly ONE next action using the decide_next_action tool:

- ask_clarification: use this ONLY when a finding's evidence says the trim rectangle is not confirmed (look for a "trim rectangle not confirmed" or similar reason in the evidence). This is the ONLY clarification this system supports - do not use it for anything else, even if it seems like a reasonable question to ask.
- request_repair: a NEEDS_REVIEW bleed finding whose evidence shows a confirmed trim with insufficient margin (available_bleed_in below required_bleed_in) may be repair-eligible.
- escalate: anything else - low resolution, an unsupported color mode, a malformed/incomplete result, or anything that isn't exactly one of the two specific cases above. When in doubt, escalate rather than guess.

Always call the tool. Never answer in plain text, and never invent a finding that wasn't given to you.`

var decisionTool = map[string]interface{}{
	"type": "function",
	"function": map[string]interface{}{
		"name":        "decide_next_action",
		"description": "Choose exactly one next action for this blocked artwork case, based only on the findings given.",
		"parameters": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type": "string",
					"enum": []string{ActionAskClarification, ActionRequestRepair, ActionEscalate},
				},
				"question": map[string]interface{}{
					"type":        "string",
					"description": "A single, targeted, plain-language yes/no question for the customer. Required only when action is ask_clarification.",
				},
			},
			"required": []string{"action"},
		},
	},
}

type groqChatRequest struct {
	Model      string                   `json:"model"`
	Messages   []groqMessage            `json:"messages"`
	Tools      []map[string]interface{} `json:"tools"`
	ToolChoice map[string]interface{}   `json:"tool_choice"`
	MaxTokens  int                      `json:"max_tokens"`
}

type groqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqChatResponse struct {
	Choices []struct {
		Message struct {
			ToolCalls []struct {
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage TokenUsage `json:"usage"`
}

type groqErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

type decisionArgs struct {
	Action   string `json:"action"`
	Question string `json:"question"`
}

func (a *GroqAdapter) Decide(ctx context.Context, in DecisionInput) (Decision, error) {
	findingsJSON, err := json.Marshal(in.Findings)
	if err != nil {
		return Decision{}, permanentErr(fmt.Errorf("failed to encode findings: %w", err))
	}
	userPrompt := fmt.Sprintf(
		"Order: %s sticker, declared %.3fx%.3f %s, intent=%s.\nFindings:\n%s",
		in.ProductType, in.DeclaredWidth, in.DeclaredHeight, in.DeclaredUnit, in.Intent, string(findingsJSON),
	)

	reqBody := groqChatRequest{
		Model: a.model,
		Messages: []groqMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Tools: []map[string]interface{}{decisionTool},
		ToolChoice: map[string]interface{}{
			"type":     "function",
			"function": map[string]string{"name": "decide_next_action"},
		},
		MaxTokens: 500,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return Decision{}, permanentErr(fmt.Errorf("failed to encode request: %w", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return Decision{}, permanentErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.http.Do(req)
	if err != nil {
		// No response at all (network error, timeout, connection refused) -
		// always worth retrying.
		return Decision{}, transientErr(fmt.Errorf("groq request failed: %w", err))
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return Decision{}, transientErr(fmt.Errorf("failed to read groq response body: %w", err))
	}

	if resp.StatusCode != http.StatusOK {
		return Decision{}, classifyHTTPError(resp.StatusCode, respBytes)
	}

	var parsed groqChatResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return Decision{}, permanentErr(fmt.Errorf("failed to parse groq response: %w", err))
	}
	if len(parsed.Choices) == 0 || len(parsed.Choices[0].Message.ToolCalls) == 0 {
		// A 200 with no tool call despite tool_choice being forced is a
		// model-behavior hiccup, not a request problem - worth retrying.
		return Decision{}, transientErr(fmt.Errorf("groq did not call the decision tool"))
	}

	var args decisionArgs
	if err := json.Unmarshal([]byte(parsed.Choices[0].Message.ToolCalls[0].Function.Arguments), &args); err != nil {
		return Decision{}, permanentErr(fmt.Errorf("failed to parse decision arguments: %w", err))
	}

	switch args.Action {
	case ActionAskClarification, ActionRequestRepair, ActionEscalate:
	default:
		return Decision{}, permanentErr(fmt.Errorf("model returned unrecognized action %q", args.Action))
	}

	return Decision{Action: args.Action, Question: args.Question, TokenUsage: parsed.Usage}, nil
}

// classifyHTTPError distinguishes auth/invalid-request failures (never
// worth retrying - the same key or the same malformed request will fail
// again) from rate limits, server errors, and the one specific "model
// didn't call the required tool" case Groq reports as a 400 (a model
// hiccup, worth retrying).
func classifyHTTPError(status int, body []byte) error {
	baseErr := fmt.Errorf("groq returned %d: %s", status, string(body))

	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return permanentErr(baseErr) // bad/revoked API key
	case status == http.StatusNotFound:
		return permanentErr(baseErr) // bad model name or endpoint
	case status == http.StatusTooManyRequests:
		return transientErr(baseErr) // rate limited
	case status >= 500:
		return transientErr(baseErr) // server-side error
	case status == http.StatusBadRequest:
		var parsed groqErrorBody
		if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Code == "tool_use_failed" {
			return transientErr(baseErr)
		}
		return permanentErr(baseErr) // malformed request - a bug in our own code, not fixed by retrying
	default:
		return permanentErr(baseErr)
	}
}
