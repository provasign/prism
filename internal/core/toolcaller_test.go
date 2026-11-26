package core

import (
	"context"
	"encoding/json"
)

// toolCaller drives tools/call through CallTool and returns what the MCP
// server returns for it, so core's tool tests read the same result shape.
// The JSON-RPC layer itself (initialize, tools/list, which names each server
// exposes) is internal/mcp and is tested there.
type toolCaller struct {
	handler *Handler
	compact bool
}

type callError struct {
	Code    int
	Message string
}

func newCompactCaller(h *Handler) *toolCaller { return &toolCaller{handler: h, compact: true} }

func (c *toolCaller) dispatch(method string, params json.RawMessage) (any, *callError) {
	if method != "tools/call" {
		panic("toolCaller answers tools/call only; test " + method + " in internal/mcp")
	}
	var call struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &callError{Code: -32602, Message: err.Error()}
	}
	answer, err := c.handler.CallTool(context.Background(), c.compact, call.Name, call.Arguments)
	if err != nil {
		return nil, &callError{Code: -32602, Message: err.Error()}
	}
	content := make([]map[string]string, 0, len(answer.Texts))
	for _, text := range answer.Texts {
		content = append(content, map[string]string{"type": "text", "text": text})
	}
	if answer.IsError {
		return map[string]any{"content": content, "isError": true}, nil
	}
	return map[string]any{"content": content}, nil
}
