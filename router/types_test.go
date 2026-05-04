package main

import (
	"encoding/json"
	"testing"
)

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
