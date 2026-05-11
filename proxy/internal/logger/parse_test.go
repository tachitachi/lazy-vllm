package logger

import (
	"encoding/json"
	"testing"
)

// ── ParseMessages (OpenAI request) ────────────────────────────────────────────

func TestParseMessages(t *testing.T) {
	tests := []struct {
		name   string
		body   []byte
		length int
	}{
		{
			name:   "simple user message",
			body:   []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			length: 1,
		},
		{
			name: "multi-turn",
			body: []byte(`{"messages":[
				{"role":"user","content":"hi"},
				{"role":"assistant","content":"hello"},
				{"role":"user","content":"tell me more"}
			]}`),
			length: 3,
		},
		{
			name:   "empty messages",
			body:   []byte(`{"messages":[]}`),
			length: 0,
		},
		{
			name:   "missing messages key",
			body:   []byte(`{"model":"test"}`),
			length: 0,
		},
		{
			name:   "invalid json",
			body:   []byte(`{bad}`),
			length: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := ParseMessages(tt.body)
			if len(msgs) != tt.length {
				t.Errorf("ParseMessages() returned %d messages, want %d", len(msgs), tt.length)
			}
		})
	}
}

// ── ParseAnthropicMessages ────────────────────────────────────────────────────

func TestParseAnthropicMessages_UserText(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	msgs := ParseAnthropicMessages(body)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("role = %q, want %q", msgs[0].Role, "user")
	}
}

func TestParseAnthropicMessages_UserBlocks(t *testing.T) {
	body := []byte(`{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "hello"},
				{"type": "tool_result", "tool_use_id": "call_123", "content": "42"}
			]
		}]
	}`)
	msgs := ParseAnthropicMessages(body)
	// tool_result → tool message, text → user message = 2 messages
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d: %v", len(msgs), msgs)
	}
	if msgs[0].Role != "tool" {
		t.Errorf("msg[0] role = %q, want %q", msgs[0].Role, "tool")
	}
	if msgs[0].ToolCallID != "call_123" {
		t.Errorf("tool_call_id = %q, want %q", msgs[0].ToolCallID, "call_123")
	}
	if msgs[1].Role != "user" {
		t.Errorf("msg[1] role = %q, want %q", msgs[1].Role, "user")
	}
}

func TestParseAnthropicMessages_AssistantBlocks(t *testing.T) {
	body := []byte(`{
		"messages": [{
			"role": "assistant",
			"content": [
				{"type": "thinking", "thinking": "let me think"},
				{"type": "text", "text": "hello world"},
				{"type": "tool_use", "id": "call_456", "name": "search", "input": {"q": "test"}}
			]
		}]
	}`)
	msgs := ParseAnthropicMessages(body)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "assistant" {
		t.Errorf("role = %q, want %q", msg.Role, "assistant")
	}
	if msg.ReasoningContent != "let me think" {
		t.Errorf("reasoning = %q, want %q", msg.ReasoningContent, "let me think")
	}
	if string(msg.Content) != `"hello world"` {
		t.Errorf("content = %q, want %q", msg.Content, `"hello world"`)
	}
	var tc []ToolCall
	if err := json.Unmarshal(msg.ToolCalls, &tc); err != nil {
		t.Fatalf("unmarshal tool_calls: %v", err)
	}
	if len(tc) != 1 {
		t.Fatalf("tool_calls = %v, expected 1 call", tc)
	}
	if tc[0].ID != "call_456" {
		t.Errorf("tool_call id = %q, want %q", tc[0].ID, "call_456")
	}
	if tc[0].Function.Name != "search" {
		t.Errorf("tool_call name = %q, want %q", tc[0].Function.Name, "search")
	}
}

func TestParseAnthropicMessages_System(t *testing.T) {
	body := []byte(`{
		"system": "You are a helpful assistant",
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	msgs := ParseAnthropicMessages(body)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("msg[0] role = %q, want %q", msgs[0].Role, "system")
	}
}

func TestParseAnthropicMessages_SystemBlockArray(t *testing.T) {
	body := []byte(`{
		"system": [
			{"type": "text", "text": "Be concise"},
			{"type": "text", "text": "No extra fluff"}
		],
		"messages": [{"role": "user", "content": "hi"}]
	}`)
	msgs := ParseAnthropicMessages(body)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if string(msgs[0].Content) != `"Be conciseNo extra fluff"` {
		t.Errorf("system content = %q, want %q", msgs[0].Content, `"Be conciseNo extra fluff"`)
	}
}

func TestParseAnthropicMessages_ToolOnly(t *testing.T) {
	body := []byte(`{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "tool_result", "tool_use_id": "abc", "content": "error message"}
			]
		}]
	}`)
	msgs := ParseAnthropicMessages(body)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "tool" {
		t.Errorf("role = %q, want %q", msgs[0].Role, "tool")
	}
	if msgs[0].ToolCallID != "abc" {
		t.Errorf("tool_call_id = %q, want %q", msgs[0].ToolCallID, "abc")
	}
}

func TestParseAnthropicMessages_Empty(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{}`),
		[]byte(`{"messages":[]}`),
		[]byte(`not json`),
	} {
		msgs := ParseAnthropicMessages(body)
		if len(msgs) != 0 {
			t.Errorf("ParseAnthropicMessages(%q) = %d messages, want 0", body, len(msgs))
		}
	}
}

// ── ParseOpenAIOutput ─────────────────────────────────────────────────────────

func TestParseOpenAIOutput_JSON(t *testing.T) {
	body := []byte(`{
		"choices": [{
			"message": {
				"content": "hello world",
				"reasoning": "let me calculate",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "get_weather", "arguments": "{\"city\":\"NYC\"}"}
				}]
			}
		}]
	}`)
	out := ParseOpenAIOutput(body, false)
	if out.Content != "hello world" {
		t.Errorf("content = %q, want %q", out.Content, "hello world")
	}
	if out.Reasoning != "let me calculate" {
		t.Errorf("reasoning = %q, want %q", out.Reasoning, "let me calculate")
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(out.ToolCalls))
	}
	if out.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("tool name = %q, want %q", out.ToolCalls[0].Function.Name, "get_weather")
	}
}

func TestParseOpenAIOutput_JSON_Empty(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"choices":[]}`),
		[]byte(`{}`),
		[]byte(`invalid`),
	} {
		out := ParseOpenAIOutput(body, false)
		if out.Content != "" || len(out.ToolCalls) != 0 {
			t.Errorf("ParseOpenAIOutput(%q) = %+v, want empty", body, out)
		}
	}
}

func TestParseOpenAIOutput_SSE(t *testing.T) {
	body := []byte(`data: {"choices":[{"delta":{"content":"hel"}}]}
data: {"choices":[{"delta":{"content":"lo"}}]}
data: {"choices":[{"delta":{"reasoning":"calc"}}]}
data: [DONE]`)

	out := ParseOpenAIOutput(body, true)
	if out.Content != "hello" {
		t.Errorf("content = %q, want %q", out.Content, "hello")
	}
	if out.Reasoning != "calc" {
		t.Errorf("reasoning = %q, want %q", out.Reasoning, "calc")
	}
}

func TestParseOpenAIOutput_SSE_ToolCalls(t *testing.T) {
	first := `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function",`
	second := `"function":{"name":"foo"}}]}}]}`
	body := []byte(first + second + "\ndata: " +
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"arg1"}}]}}]}` +
		"\ndata: " +
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"arg2"}}]}}]}`)

	out := ParseOpenAIOutput(body, true)
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(out.ToolCalls))
	}
	tc := out.ToolCalls[0]
	if tc.ID != "call_1" {
		t.Errorf("tool id = %q, want %q", tc.ID, "call_1")
	}
	if tc.Function.Name != "foo" {
		t.Errorf("tool name = %q, want %q", tc.Function.Name, "foo")
	}
	if tc.Function.Arguments != "arg1arg2" {
		t.Errorf("arguments = %q, want %q", tc.Function.Arguments, "arg1arg2")
	}
}

func TestParseOpenAIOutput_SSE_Malformed(t *testing.T) {
	// Lines that aren't SSE data lines should be silently skipped.
	body := []byte(`some garbage
no data prefix here
data: {"choices":[]}
`)
	out := ParseOpenAIOutput(body, true)
	if out.Content != "" {
		t.Errorf("expected empty content from malformed SSE, got %q", out.Content)
	}
}

// ── ParseAnthropicOutput ──────────────────────────────────────────────────────

func TestParseAnthropicOutput_JSON(t *testing.T) {
	body := []byte(`{
		"content": [
			{"type": "text", "text": "hello"},
			{"type": "thinking", "thinking": "figuring this out"},
			{"type": "tool_use", "id": "blk_1", "name": "run", "input": {"cmd": "ls"}}
		]
	}`)
	out := ParseAnthropicOutput(body, false)
	if out.Content != "hello" {
		t.Errorf("content = %q, want %q", out.Content, "hello")
	}
	if out.Reasoning != "figuring this out" {
		t.Errorf("reasoning = %q, want %q", out.Reasoning, "figuring this out")
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(out.ToolCalls))
	}
	if out.ToolCalls[0].ID != "blk_1" {
		t.Errorf("id = %q, want %q", out.ToolCalls[0].ID, "blk_1")
	}
	if out.ToolCalls[0].Function.Name != "run" {
		t.Errorf("name = %q, want %q", out.ToolCalls[0].Function.Name, "run")
	}
}

func TestParseAnthropicOutput_JSON_Empty(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"content":[]}`),
		[]byte(`{}`),
		[]byte(`invalid`),
	} {
		out := ParseAnthropicOutput(body, false)
		if out.Content != "" || out.Reasoning != "" || len(out.ToolCalls) != 0 {
			t.Errorf("ParseAnthropicOutput(%q) = %+v, want empty", body, out)
		}
	}
}

func TestParseAnthropicOutput_SSE(t *testing.T) {
	// Build the SSE data without nested quote escaping issues.
	data := "data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"text\",\"id\":\"blk1\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\" world\"}}\n" +
		"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"thinking...\"}}\n" +
		"data: {\"type\":\"content_block_start\",\"content_block\":{\"type\":\"tool_use\",\"id\":\"blk2\",\"name\":\"calc\"}}"
	// Partial JSON for tool — use a numeric value to avoid nested quote escaping.
	data += "\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"123\"}}"

	body := []byte(data)

	out := ParseAnthropicOutput(body, true)
	if out.Content != "hello world" {
		t.Errorf("content = %q, want %q", out.Content, "hello world")
	}
	if out.Reasoning != "thinking..." {
		t.Errorf("reasoning = %q, want %q", out.Reasoning, "thinking...")
	}
	if len(out.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(out.ToolCalls))
	}
	tc := out.ToolCalls[0]
	if tc.ID != "blk2" {
		t.Errorf("id = %q, want %q", tc.ID, "blk2")
	}
	if tc.Function.Name != "calc" {
		t.Errorf("name = %q, want %q", tc.Function.Name, "calc")
	}
	if tc.Function.Arguments != "123" {
		t.Errorf("arguments = %q, want %q", tc.Function.Arguments, "123")
	}
}

func TestParseAnthropicOutput_SSE_NoText(t *testing.T) {
	// Only tool deltas, no text content.
	body := []byte(`data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"blk2","name":"f"}}
data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"x"}}`)
	out := ParseAnthropicOutput(body, true)
	if out.Content != "" {
		t.Errorf("expected empty content, got %q", out.Content)
	}
	if len(out.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(out.ToolCalls))
	}
}

func TestParseAnthropicOutput_SSE_Malformed(t *testing.T) {
	body := []byte(`invalid json
data: not-json
data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"good"}}`)
	out := ParseAnthropicOutput(body, true)
	if out.Content != "good" {
		t.Errorf("expected 'good', got %q", out.Content)
	}
}

// ── sortedToolCalls ───────────────────────────────────────────────────────────

func TestSortedToolCalls(t *testing.T) {
	tools := map[int]*ToolCall{
		2: {ID: "c"},
		0: {ID: "a"},
		1: {ID: "b"},
	}
	result := sortedToolCalls(tools)
	if len(result) != 3 {
		t.Fatalf("expected 3 tool calls, got %d", len(result))
	}
	for i, tc := range result {
		if tc.ID != string(rune('a'+i)) {
			t.Errorf("tool[%d].ID = %q, want %q", i, tc.ID, string(rune('a'+i)))
		}
	}
}

func TestSortedToolCalls_Empty(t *testing.T) {
	result := sortedToolCalls(nil)
	if result != nil {
		t.Errorf("sortedToolCalls(nil) = %v, want nil", result)
	}

	result = sortedToolCalls(make(map[int]*ToolCall))
	if result != nil {
		t.Errorf("sortedToolCalls(empty) = %v, want nil", result)
	}
}

// ── extractSystemText ─────────────────────────────────────────────────────────

func TestExtractSystemText(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "plain string",
			raw:  json.RawMessage(`"be concise"`),
			want: "be concise",
		},
		{
			name: "block array",
			raw:  json.RawMessage(`[{"type":"text","text":"be"},{"type":"text","text":" concise"}]`),
			want: "be concise",
		},
		{
			name: "empty",
			raw:  nil,
			want: "",
		},
		{
			name: "blank",
			raw:  json.RawMessage(`""`),
			want: "",
		},
		{
			name: "unrecognized format",
			raw:  json.RawMessage(`[1,2,3]`),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSystemText(tt.raw)
			if got != tt.want {
				t.Errorf("extractSystemText(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

// ── ParseMessages with tool calls and reasoning ───────────────────────────────

func TestParseMessages_ToolCalls(t *testing.T) {
	body := []byte(`{
		"messages": [{
			"role": "assistant",
			"content": "result",
			"tool_calls": [{
				"id": "call_1",
				"type": "function",
				"function": {"name": "calc", "arguments": "1+1"}
			}]
		}]
	}`)
	msgs := ParseMessages(body)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	var tc []ToolCall
	if err := json.Unmarshal(msg.ToolCalls, &tc); err != nil {
		t.Fatalf("unmarshal tool_calls: %v", err)
	}
	if len(tc) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(tc))
	}
	if tc[0].ID != "call_1" {
		t.Errorf("tool_call id = %q, want %q", tc[0].ID, "call_1")
	}
	if tc[0].Function.Name != "calc" {
		t.Errorf("tool_call name = %q, want %q", tc[0].Function.Name, "calc")
	}
	if tc[0].Function.Arguments != "1+1" {
		t.Errorf("arguments = %q, want %q", tc[0].Function.Arguments, "1+1")
	}
}

// ── ParseAnthropicMessages with tool result containing sub-blocks ──────────────

func TestParseAnthropicMessages_ToolResultSubBlocks(t *testing.T) {
	body := []byte(`{
		"messages": [{
			"role": "user",
			"content": [{
				"type": "tool_result",
				"tool_use_id": "call_123",
				"content": [{"type": "text", "text": "parsed text"}]
			}]
		}]
	}`)
	msgs := ParseAnthropicMessages(body)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "tool" {
		t.Errorf("role = %q, want %q", msg.Role, "tool")
	}
	if msg.ToolCallID != "call_123" {
		t.Errorf("tool_call_id = %q, want %q", msg.ToolCallID, "call_123")
	}
	if string(msg.Content) != `"parsed text"` {
		t.Errorf("content = %q, want %q", msg.Content, `"parsed text"`)
	}
}
