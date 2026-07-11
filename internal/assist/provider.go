// Package assist is the model-agnostic harness: a natural-language task is
// routed by ANY chat model (local Ollama, Anthropic, OpenAI) to prism's
// deterministic code-graph operations. The discipline that steering files can
// only request is enforced here structurally: the model never receives or
// re-emits operation payloads (the harness renders them), and no text-search
// escape hatch exists inside the loop — the two measured failure modes of
// steered agents (relay loss, re-derivation) are impossible by construction.
package assist

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Msg is one chat message in the provider-neutral shape.
type Msg struct {
	Role    string     // "system" | "user" | "assistant" | "tool"
	Content string     // text content (or tool result payload for Role=="tool")
	Calls   []ToolCall // tool calls the assistant issued (Role=="assistant")
	CallID  string     // for Role=="tool": which call this result answers
	Name    string     // for Role=="tool": the tool name
}

// ToolCall is a provider-neutral tool invocation.
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

// ToolDef is a provider-neutral tool definition (JSON-schema parameters).
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Provider abstracts one chat turn against any model backend.
type Provider interface {
	// Chat sends the conversation and returns the assistant's reply.
	// forceTools requests that the model MUST call a tool this turn (the
	// invocation wall for small local models); providers that cannot express
	// it may ignore it.
	Chat(msgs []Msg, tools []ToolDef, forceTools bool) (Msg, error)
	Name() string
}

var httpc = &http.Client{Timeout: 10 * time.Minute}

func postJSON(url string, headers map[string]string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: HTTP %d: %s", url, resp.StatusCode, truncate(string(out), 300))
	}
	return out, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// NewProvider resolves a model spec to a provider:
//
//	ollama:<model>     local Ollama (default http://localhost:11434)
//	claude:<model>     Anthropic API (ANTHROPIC_API_KEY)
//	openai:<model>     OpenAI API (OPENAI_API_KEY)
//
// A bare spec with no prefix is treated as an Ollama model name.
func NewProvider(spec string) (Provider, error) {
	kind, model, found := strings.Cut(spec, ":")
	if !found {
		kind, model = "ollama", spec
	}
	switch kind {
	case "ollama":
		return &ollamaProvider{model: model, url: envOr("OLLAMA_HOST_URL", "http://localhost:11434")}, nil
	case "claude", "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("claude:%s needs ANTHROPIC_API_KEY", model)
		}
		return &anthropicProvider{model: model, key: key}, nil
	case "openai", "gpt":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("openai:%s needs OPENAI_API_KEY", model)
		}
		return &openaiProvider{model: model, key: key}, nil
	default:
		// "qwen3-coder:30b" — the whole spec is an Ollama model tag.
		return &ollamaProvider{model: spec, url: envOr("OLLAMA_HOST_URL", "http://localhost:11434")}, nil
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// --- Ollama ---------------------------------------------------------------

type ollamaProvider struct {
	model string
	url   string
}

func (p *ollamaProvider) Name() string { return "ollama:" + p.model }

func (p *ollamaProvider) Chat(msgs []Msg, tools []ToolDef, forceTools bool) (Msg, error) {
	messages := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		mm := map[string]any{"role": m.Role, "content": m.Content}
		if len(m.Calls) > 0 {
			var calls []map[string]any
			for _, c := range m.Calls {
				calls = append(calls, map[string]any{
					"function": map[string]any{"name": c.Name, "arguments": c.Args},
				})
			}
			mm["tool_calls"] = calls
		}
		messages = append(messages, mm)
	}
	var tdefs []map[string]any
	for _, t := range tools {
		tdefs = append(tdefs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Parameters,
			},
		})
	}
	payload := map[string]any{
		"model": p.model, "messages": messages, "tools": tdefs, "stream": false,
		"options": map[string]any{"temperature": 0, "num_ctx": 16384},
	}
	if forceTools {
		payload["tool_choice"] = "required"
	}
	raw, err := postJSON(p.url+"/api/chat", nil, payload)
	if err != nil {
		return Msg{}, err
	}
	var resp struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Msg{}, err
	}
	out := Msg{Role: "assistant", Content: resp.Message.Content}
	for i, c := range resp.Message.ToolCalls {
		args := map[string]any{}
		_ = json.Unmarshal(c.Function.Arguments, &args)
		out.Calls = append(out.Calls, ToolCall{
			ID: fmt.Sprintf("call_%d", i), Name: c.Function.Name, Args: args,
		})
	}
	// Some Ollama model templates never populate tool_calls and emit the call
	// as raw JSON in content instead (qwen2.5-coder:14b). The model's DECISION
	// is correct; only the serialization is nonstandard — parse it. Verified:
	// without this fallback a correct first-try routing scored a false 0.
	if len(out.Calls) == 0 {
		if c := parseContentToolCall(out.Content, tools); c != nil {
			out.Calls = append(out.Calls, *c)
			out.Content = ""
		}
	}
	return out, nil
}

// parseContentToolCall recognizes `{"name": <known tool>, "arguments": {...}}`
// (optionally fenced) emitted as plain content and converts it to a ToolCall.
func parseContentToolCall(content string, tools []ToolDef) *ToolCall {
	s := strings.TrimSpace(content)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return nil
	}
	var obj struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(s), &obj); err != nil || obj.Name == "" {
		return nil
	}
	for _, t := range tools {
		if t.Name == obj.Name {
			args := obj.Arguments
			if args == nil {
				args = map[string]any{}
			}
			return &ToolCall{ID: "call_content", Name: obj.Name, Args: args}
		}
	}
	return nil
}

// --- Anthropic --------------------------------------------------------------

type anthropicProvider struct {
	model string
	key   string
}

func (p *anthropicProvider) Name() string { return "claude:" + p.model }

func (p *anthropicProvider) Chat(msgs []Msg, tools []ToolDef, forceTools bool) (Msg, error) {
	var system string
	var messages []map[string]any
	for _, m := range msgs {
		switch m.Role {
		case "system":
			system = m.Content
		case "assistant":
			blocks := []map[string]any{}
			if m.Content != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
			}
			for _, c := range m.Calls {
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": c.ID, "name": c.Name, "input": c.Args,
				})
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
		case "tool":
			messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{{
				"type": "tool_result", "tool_use_id": m.CallID, "content": m.Content,
			}}})
		default:
			messages = append(messages, map[string]any{"role": "user", "content": m.Content})
		}
	}
	var tdefs []map[string]any
	for _, t := range tools {
		tdefs = append(tdefs, map[string]any{
			"name": t.Name, "description": t.Description, "input_schema": t.Parameters,
		})
	}
	payload := map[string]any{
		"model": p.model, "max_tokens": 2048, "system": system,
		"messages": messages, "tools": tdefs,
	}
	if forceTools {
		payload["tool_choice"] = map[string]any{"type": "any"}
	}
	raw, err := postJSON("https://api.anthropic.com/v1/messages", map[string]string{
		"x-api-key": p.key, "anthropic-version": "2023-06-01",
	}, payload)
	if err != nil {
		return Msg{}, err
	}
	var resp struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Msg{}, err
	}
	out := Msg{Role: "assistant"}
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			out.Content += b.Text
		case "tool_use":
			out.Calls = append(out.Calls, ToolCall{ID: b.ID, Name: b.Name, Args: b.Input})
		}
	}
	return out, nil
}

// --- OpenAI -----------------------------------------------------------------

type openaiProvider struct {
	model string
	key   string
}

func (p *openaiProvider) Name() string { return "openai:" + p.model }

func (p *openaiProvider) Chat(msgs []Msg, tools []ToolDef, forceTools bool) (Msg, error) {
	var messages []map[string]any
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			mm := map[string]any{"role": "assistant", "content": m.Content}
			if len(m.Calls) > 0 {
				var calls []map[string]any
				for _, c := range m.Calls {
					args, _ := json.Marshal(c.Args)
					calls = append(calls, map[string]any{
						"id": c.ID, "type": "function",
						"function": map[string]any{"name": c.Name, "arguments": string(args)},
					})
				}
				mm["tool_calls"] = calls
			}
			messages = append(messages, mm)
		case "tool":
			messages = append(messages, map[string]any{
				"role": "tool", "tool_call_id": m.CallID, "content": m.Content,
			})
		default:
			messages = append(messages, map[string]any{"role": m.Role, "content": m.Content})
		}
	}
	var tdefs []map[string]any
	for _, t := range tools {
		tdefs = append(tdefs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": t.Parameters,
			},
		})
	}
	payload := map[string]any{"model": p.model, "messages": messages, "tools": tdefs}
	if forceTools {
		payload["tool_choice"] = "required"
	}
	raw, err := postJSON("https://api.openai.com/v1/chat/completions", map[string]string{
		"Authorization": "Bearer " + p.key,
	}, payload)
	if err != nil {
		return Msg{}, err
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Msg{}, err
	}
	if len(resp.Choices) == 0 {
		return Msg{}, fmt.Errorf("openai: empty choices")
	}
	m := resp.Choices[0].Message
	out := Msg{Role: "assistant", Content: m.Content}
	for _, c := range m.ToolCalls {
		args := map[string]any{}
		_ = json.Unmarshal([]byte(c.Function.Arguments), &args)
		out.Calls = append(out.Calls, ToolCall{ID: c.ID, Name: c.Function.Name, Args: args})
	}
	return out, nil
}

// DetectDefaultModel picks a provider when --model is not given: a blessed
// local Ollama model if one is installed (free, private), else Anthropic's
// small tier, else OpenAI. The blessed list is the measured set — models
// verified to sit on the engine ceiling at task altitude.
func DetectDefaultModel() (string, error) {
	if raw, err := postJSONGet("http://localhost:11434/api/tags"); err == nil {
		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if json.Unmarshal(raw, &tags) == nil {
			installed := map[string]bool{}
			for _, m := range tags.Models {
				installed[m.Name] = true
			}
			for _, blessed := range []string{"qwen3-coder:30b", "qwen2.5-coder:14b"} {
				if installed[blessed] {
					return "ollama:" + blessed, nil
				}
			}
		}
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "claude:claude-haiku-4-5-20251001", nil
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		return "openai:gpt-4o-mini", nil
	}
	return "", fmt.Errorf("no model available: install a blessed Ollama model " +
		"(qwen3-coder:30b or qwen2.5-coder:14b), or set ANTHROPIC_API_KEY / " +
		"OPENAI_API_KEY, or pass --model explicitly")
}

func postJSONGet(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
