package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// assertEnableThinking verifies body contains chat_template_kwargs.enable_thinking == want.
func assertEnableThinking(t *testing.T, body []byte, want bool) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatalf("body is not valid JSON: %v", err)
	}
	raw, ok := top["chat_template_kwargs"]
	if !ok {
		t.Fatal("chat_template_kwargs missing from body")
	}
	var kwargs map[string]bool
	if err := json.Unmarshal(raw, &kwargs); err != nil {
		t.Fatalf("chat_template_kwargs is not valid JSON object: %v", err)
	}
	if kwargs["enable_thinking"] != want {
		t.Errorf("enable_thinking: got %v, want %v", kwargs["enable_thinking"], want)
	}
}

// vllmClassifyResponse returns a vLLM-format chat completion response with the given content.
func vllmClassifyResponse(content string) []byte {
	b, _ := json.Marshal(chatResponse{
		Choices: []chatChoice{{Message: ChatMessage{Role: "assistant", Content: content}}},
	})
	return b
}

// vllmCompletionResponse returns a minimal vLLM completion response.
func vllmCompletionResponse() []byte {
	b, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{{
			"message": map[string]any{"role": "assistant", "content": "ok"},
		}},
	})
	return b
}

// testConfig returns a config pointing at the given URL with sensible test defaults.
func testConfig(url string) config {
	return config{VLLMBaseURL: url, ModelName: "test-model", WindowSize: 3}
}

// dualHandler returns an HTTP handler that distinguishes classify calls from main
// completion calls by checking for "structured_outputs" in the request body.
// classifyContent is returned as the classification response.
// captureMain, if non-nil, receives the raw body bytes of the main call.
func dualHandler(classifyContent string, completionResp []byte, captureMain *[]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var top map[string]json.RawMessage
		json.Unmarshal(body, &top)
		if _, isClassify := top["structured_outputs"]; isClassify {
			w.Write(vllmClassifyResponse(classifyContent))
		} else {
			if captureMain != nil {
				*captureMain = body
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(completionResp)
		}
	}
}
