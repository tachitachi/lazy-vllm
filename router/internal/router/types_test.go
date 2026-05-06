package router

import (
	"encoding/json"
	"testing"
)

// --- extractText ---

func TestExtractText_String(t *testing.T) {
	if got := extractText("hello world"); got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestExtractText_ContentBlockArray(t *testing.T) {
	// Simulate JSON-decoded []any as produced by json.Unmarshal into any.
	blocks := []any{
		map[string]any{"type": "text", "text": "hello"},
		map[string]any{"type": "text", "text": "world"},
	}
	if got := extractText(blocks); got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestExtractText_NonTextBlocksSkipped(t *testing.T) {
	blocks := []any{
		map[string]any{"type": "image", "source": map[string]any{}},
		map[string]any{"type": "text", "text": "text only"},
	}
	if got := extractText(blocks); got != "text only" {
		t.Errorf("got %q, want %q", got, "text only")
	}
}

func TestExtractText_NilReturnsEmpty(t *testing.T) {
	if got := extractText(nil); got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

// --- anthropicMessagesToChat ---

func TestAnthropicMessagesToChat_StringContent(t *testing.T) {
	msgs := []AnthropicMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}
	result := anthropicMessagesToChat(nil, msgs)
	if len(result) != 2 {
		t.Fatalf("len: got %d, want 2", len(result))
	}
	if result[0].Role != "user" || result[0].Content != "hello" {
		t.Errorf("msg[0]: got {%s %v}", result[0].Role, result[0].Content)
	}
}

func TestAnthropicMessagesToChat_WithSystemField(t *testing.T) {
	result := anthropicMessagesToChat("be helpful", []AnthropicMessage{
		{Role: "user", Content: "ping"},
	})
	if len(result) != 2 {
		t.Fatalf("len: got %d, want 2", len(result))
	}
	if result[0].Role != "system" || result[0].Content != "be helpful" {
		t.Errorf("system msg: got {%s %v}", result[0].Role, result[0].Content)
	}
}

func TestAnthropicMessagesToChat_EmptySystemOmitted(t *testing.T) {
	result := anthropicMessagesToChat("", []AnthropicMessage{
		{Role: "user", Content: "hi"},
	})
	if len(result) != 1 {
		t.Fatalf("len: got %d, want 1 (no system message)", len(result))
	}
	if result[0].Role != "user" {
		t.Errorf("expected user role, got %s", result[0].Role)
	}
}

func TestAnthropicMessagesToChat_ContentBlocksFlattened(t *testing.T) {
	// Simulate what json.Unmarshal produces for a content block array.
	contentBlocks := []any{
		map[string]any{"type": "text", "text": "explain this"},
	}
	result := anthropicMessagesToChat(nil, []AnthropicMessage{
		{Role: "user", Content: contentBlocks},
	})
	if len(result) != 1 {
		t.Fatalf("len: got %d, want 1", len(result))
	}
	if result[0].Content != "explain this" {
		t.Errorf("content: got %v, want %q", result[0].Content, "explain this")
	}
}

func TestAnthropicMessagesToChat_RoundtripFromJSON(t *testing.T) {
	// Verify that real JSON unmarshaling into minimalAnthropicRequest produces
	// the expected ChatMessages for the classifier.
	raw := `{"system":"you are helpful","messages":[{"role":"user","content":"hello"}]}`
	var req minimalAnthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	result := anthropicMessagesToChat(req.System, req.Messages)
	if len(result) != 2 {
		t.Fatalf("len: got %d, want 2", len(result))
	}
	if result[0].Role != "system" || result[0].Content != "you are helpful" {
		t.Errorf("system: got {%s %v}", result[0].Role, result[0].Content)
	}
	if result[1].Role != "user" || result[1].Content != "hello" {
		t.Errorf("user: got {%s %v}", result[1].Role, result[1].Content)
	}
}

func TestInjectThinkingMode(t *testing.T) {
	t.Run("sets enable_thinking true", func(t *testing.T) {
		result, err := injectThinkingMode([]byte(`{"model":"test","messages":[]}`), true)
		if err != nil {
			t.Fatal(err)
		}
		assertEnableThinking(t, result, true)
	})

	t.Run("sets enable_thinking false", func(t *testing.T) {
		result, err := injectThinkingMode([]byte(`{"model":"test","messages":[]}`), false)
		if err != nil {
			t.Fatal(err)
		}
		assertEnableThinking(t, result, false)
	})

	t.Run("overwrites existing chat_template_kwargs entirely", func(t *testing.T) {
		// The implementation replaces the whole object, not just enable_thinking.
		input := `{"model":"test","chat_template_kwargs":{"enable_thinking":true}}`
		result, err := injectThinkingMode([]byte(input), false)
		if err != nil {
			t.Fatal(err)
		}
		assertEnableThinking(t, result, false)
	})

	t.Run("preserves all other top-level fields", func(t *testing.T) {
		input := `{"model":"gpt-4","stream":true,"temperature":0.5,"tools":[],"extra_body":{"x":1}}`
		result, err := injectThinkingMode([]byte(input), true)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]json.RawMessage
		if err := json.Unmarshal(result, &out); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"model", "stream", "temperature", "tools", "extra_body"} {
			if _, ok := out[field]; !ok {
				t.Errorf("field %q was not preserved after injection", field)
			}
		}
	})

	t.Run("returns error on invalid JSON", func(t *testing.T) {
		_, err := injectThinkingMode([]byte(`not json`), true)
		if err == nil {
			t.Error("expected error for invalid JSON input")
		}
	})
}
