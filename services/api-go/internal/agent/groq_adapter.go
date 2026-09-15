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
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 20 * time.Second},
		baseURL: "https://api.groq.com/openai/v1/chat/completions",
	}
}

const systemPrompt = `You coordinate an artwork exception case for a sticker order. You NEVER compute or override a measurement - the findings given to you are already deterministic and final, produced by rules code you cannot see or influence. Your only job is to choose exactly ONE next action using the decide_next_action tool:

- ask_clarification: a NEEDS_INPUT finding means the customer needs to answer something before a check can even run (e.g. confirming whether their upload already includes a bleed margin). Ask ONE targeted, plain-language yes/no question about exactly that.
- request_repair: a NEEDS_REVIEW bleed finding whose evidence shows a confirmed trim with insufficient margin (available_bleed_in below required_bleed_in) may be repair-eligible.
- escalate: anything else - an unsupported color mode, a malformed/incomplete result, or anything you're not confident fits the other two cases.

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
}

type decisionArgs struct {
	Action   string `json:"action"`
	Question string `json:"question"`
}

func (a *GroqAdapter) Decide(ctx context.Context, in DecisionInput) (Decision, error) {
	findingsJSON, err := json.Marshal(in.Findings)
	if err != nil {
		return Decision{}, fmt.Errorf("failed to encode findings: %w", err)
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
		return Decision{}, fmt.Errorf("failed to encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey)

	resp, err := a.http.Do(req)
	if err != nil {
		return Decision{}, fmt.Errorf("groq request failed: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return Decision{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Decision{}, fmt.Errorf("groq returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var parsed groqChatResponse
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return Decision{}, fmt.Errorf("failed to parse groq response: %w", err)
	}
	if len(parsed.Choices) == 0 || len(parsed.Choices[0].Message.ToolCalls) == 0 {
		return Decision{}, fmt.Errorf("groq did not call the decision tool")
	}

	var args decisionArgs
	if err := json.Unmarshal([]byte(parsed.Choices[0].Message.ToolCalls[0].Function.Arguments), &args); err != nil {
		return Decision{}, fmt.Errorf("failed to parse decision arguments: %w", err)
	}

	switch args.Action {
	case ActionAskClarification, ActionRequestRepair, ActionEscalate:
	default:
		return Decision{}, fmt.Errorf("model returned unrecognized action %q", args.Action)
	}

	return Decision{Action: args.Action, Question: args.Question}, nil
}
