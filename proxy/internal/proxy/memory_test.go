package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func sseEvent(deltaType, text string) []byte {
	payload := fmt.Sprintf(
		`{"type":"content_block_delta","index":1,"delta":{"type":"%s","%s":"%s"}}`,
		deltaType,
		map[string]string{"text_delta": "text", "thinking_delta": "thinking"}[deltaType],
		text,
	)
	return []byte("data: " + payload + "\n\n")
}

func sseBlockStart(blockType string) []byte {
	return []byte(fmt.Sprintf(
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"%s\"}}\n\n",
		blockType,
	))
}

func sseBlockStop() []byte {
	return []byte("data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
}

// ── non-streaming tests ───────────────────────────────────────────────────────

func TestMemoryCapture_NonStreaming_NoTag(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, false, "anthropic")
	mc.WriteHeader(200)

	body := `{"content":[{"type":"text","text":"hello world"}]}`
	_, _ = mc.Write([]byte(body))
	got := mc.Finalize()

	if got != "" {
		t.Errorf("expected empty memory, got %q", got)
	}
	// Client should receive the full body.
	if !bytes.Contains(rr.Body.Bytes(), []byte("hello world")) {
		t.Errorf("client body missing text: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_NonStreaming_AnthropicTextBlock(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, false, "anthropic")
	mc.WriteHeader(200)

	body := `{"content":[{"type":"text","text":"The answer is 4.\n<memory>User asked 2+2, answered 4.</memory>"}]}`
	_, _ = mc.Write([]byte(body))
	got := mc.Finalize()

	if got != "User asked 2+2, answered 4." {
		t.Errorf("memory content: got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("<memory>")) {
		t.Errorf("client body should not contain <memory>: %q", rr.Body.Bytes())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("The answer is 4.")) {
		t.Errorf("client body should contain response text: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_NonStreaming_AnthropicIgnoresThinkingBlock(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, false, "anthropic")
	mc.WriteHeader(200)

	// thinking block contains <memory>; text block also has one
	body := `{"content":[{"type":"thinking","thinking":"I will write <memory>fake</memory>"},{"type":"text","text":"Real answer.\n<memory>real observation</memory>"}]}`
	_, _ = mc.Write([]byte(body))
	got := mc.Finalize()

	if got != "real observation" {
		t.Errorf("should capture from text block, got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("real observation")) {
		t.Errorf("client body should not contain memory content: %q", rr.Body.Bytes())
	}
	// Thinking block content should be untouched in client body.
	if !bytes.Contains(rr.Body.Bytes(), []byte("fake")) {
		t.Errorf("thinking block should be preserved in client body: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_NonStreaming_OpenAIIgnoresReasoning(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, false, "openai")
	mc.WriteHeader(200)

	body := `{"choices":[{"message":{"role":"assistant","content":"Answer.\n<memory>openai obs</memory>","reasoning":"I think <memory>fake</memory>"}}]}`
	_, _ = mc.Write([]byte(body))
	got := mc.Finalize()

	if got != "openai obs" {
		t.Errorf("should capture from content field, got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("openai obs")) {
		t.Errorf("memory should be stripped from client body: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_NonStreaming_FullBufferIntact(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, false, "anthropic")
	mc.WriteHeader(200)

	raw := `{"content":[{"type":"text","text":"hi<memory>obs</memory>"}]}`
	_, _ = mc.Write([]byte(raw))
	mc.Finalize()

	if mc.Full.String() != raw {
		t.Errorf("Full buffer should contain original bytes, got %q", mc.Full.String())
	}
}

// ── streaming tests ───────────────────────────────────────────────────────────

func TestMemoryCapture_Streaming_NoTag(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, true, "anthropic")

	_, _ = mc.Write(sseEvent("text_delta", "hello"))
	_, _ = mc.Write(sseEvent("text_delta", " world"))
	got := mc.Finalize()

	if got != "" {
		t.Errorf("expected empty memory, got %q", got)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("hello")) {
		t.Errorf("client body missing text: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_Streaming_TextDeltaWithTag(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, true, "anthropic")

	_, _ = mc.Write(sseEvent("text_delta", "Response here."))
	_, _ = mc.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"\\n<memory>did the thing</memory>\"}}\n\n"))
	got := mc.Finalize()

	if got != "did the thing" {
		t.Errorf("memory content: got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("did the thing")) {
		t.Errorf("client body should not contain memory: %q", rr.Body.Bytes())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("Response here.")) {
		t.Errorf("client body should contain response text: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_Streaming_ThinkingDeltaNotScanned(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, true, "anthropic")

	// thinking block fires first with <memory> — should be ignored
	_, _ = mc.Write(sseBlockStart("thinking"))
	_, _ = mc.Write(sseEvent("thinking_delta", "I plan to write <memory>fake</memory>"))
	_, _ = mc.Write(sseBlockStop())

	// text block fires second with the real <memory>
	_, _ = mc.Write(sseBlockStart("text"))
	_, _ = mc.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Real answer.\\n<memory>real obs</memory>\"}}\n\n"))
	_, _ = mc.Write(sseBlockStop())

	got := mc.Finalize()

	if got != "real obs" {
		t.Errorf("should capture from text_delta only, got %q", got)
	}
	// Client should see the thinking block content untouched.
	if !bytes.Contains(rr.Body.Bytes(), []byte("fake")) {
		t.Errorf("thinking block should reach client: %q", rr.Body.Bytes())
	}
	// Client should NOT see the memory observation.
	if bytes.Contains(rr.Body.Bytes(), []byte("real obs")) {
		t.Errorf("memory obs should not reach client: %q", rr.Body.Bytes())
	}
	// Client should see the text before <memory>.
	if !bytes.Contains(rr.Body.Bytes(), []byte("Real answer.")) {
		t.Errorf("response text should reach client: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_Streaming_ThinkingDeltaForwardedUnchanged(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, true, "anthropic")

	// Thinking block with no <memory> — should be forwarded verbatim.
	_, _ = mc.Write(sseBlockStart("thinking"))
	raw := sseEvent("thinking_delta", "normal thinking text")
	_, _ = mc.Write(raw)
	_, _ = mc.Write(sseBlockStop())
	mc.Finalize()

	if !bytes.Contains(rr.Body.Bytes(), []byte("normal thinking text")) {
		t.Errorf("thinking text should be forwarded: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_Streaming_CrossEventBoundary(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, true, "anthropic")

	// Tag split across two events: "before<mem" + "ory>summary_obs</memory>"
	_, _ = mc.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"before<mem\"}}\n\n"))
	_, _ = mc.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"ory>summary_obs</memory>\"}}\n\n"))
	got := mc.Finalize()

	if got != "summary_obs" {
		t.Errorf("memory content: got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("summary_obs")) {
		t.Errorf("memory obs should not reach client: %q", rr.Body.Bytes())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("before")) {
		t.Errorf("pre-tag text should reach client: %q", rr.Body.Bytes())
	}
}

func TestMemoryCapture_Streaming_FullBufferIntact(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newMemoryCapture(rr, true, "anthropic")

	event := sseEvent("text_delta", "hi")
	_, _ = mc.Write(event)
	mc.Finalize()

	if !bytes.Equal(mc.Full.Bytes(), event) {
		t.Errorf("Full buffer mismatch: got %q, want %q", mc.Full.Bytes(), event)
	}
}

// ── injection tests ───────────────────────────────────────────────────────────

func TestInject_Anthropic_StringSystem(t *testing.T) {
	body := `{"model":"claude","system":"Be helpful.","messages":[]}`
	out := injectMemoryInstruction([]byte(body), "anthropic")

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var sys string
	_ = json.Unmarshal(obj["system"], &sys)
	if !bytes.Contains([]byte(sys), []byte("<memory>")) {
		t.Errorf("system prompt should contain memory instruction, got: %q", sys)
	}
	if !bytes.HasPrefix([]byte(sys), []byte("Be helpful.")) {
		t.Errorf("existing system text should be preserved, got: %q", sys)
	}
}

func TestInject_Anthropic_NoSystem(t *testing.T) {
	body := `{"model":"claude","messages":[]}`
	out := injectMemoryInstruction([]byte(body), "anthropic")

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if _, ok := obj["system"]; !ok {
		t.Error("system field should have been created")
	}
}

func TestInject_Anthropic_ArraySystem(t *testing.T) {
	body := `{"model":"claude","system":[{"type":"text","text":"Existing."}],"messages":[]}`
	out := injectMemoryInstruction([]byte(body), "anthropic")

	var obj map[string]json.RawMessage
	_ = json.Unmarshal(out, &obj)
	var blocks []map[string]json.RawMessage
	_ = json.Unmarshal(obj["system"], &blocks)
	if len(blocks) == 0 {
		t.Fatal("expected blocks")
	}
	var text string
	_ = json.Unmarshal(blocks[0]["text"], &text)
	if !bytes.Contains([]byte(text), []byte("Existing.")) {
		t.Errorf("existing text should be preserved, got: %q", text)
	}
	if !bytes.Contains([]byte(text), []byte("<memory>")) {
		t.Errorf("instruction should be appended, got: %q", text)
	}
}

func TestInject_OpenAI_WithSystem(t *testing.T) {
	body := `{"model":"gpt","messages":[{"role":"system","content":"You help."},{"role":"user","content":"hi"}]}`
	out := injectMemoryInstruction([]byte(body), "openai")

	var obj map[string]json.RawMessage
	_ = json.Unmarshal(out, &obj)
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)

	var content string
	_ = json.Unmarshal(messages[0]["content"], &content)
	if !bytes.Contains([]byte(content), []byte("You help.")) {
		t.Errorf("original system content lost, got: %q", content)
	}
	if !bytes.Contains([]byte(content), []byte("<memory>")) {
		t.Errorf("instruction not appended, got: %q", content)
	}
}

func TestInject_OpenAI_WithoutSystem(t *testing.T) {
	body := `{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`
	out := injectMemoryInstruction([]byte(body), "openai")

	var obj map[string]json.RawMessage
	_ = json.Unmarshal(out, &obj)
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages (prepended system + user), got %d", len(messages))
	}
	var role string
	_ = json.Unmarshal(messages[0]["role"], &role)
	if role != "system" {
		t.Errorf("first message should be system, got %q", role)
	}
}

func TestInject_UnknownFormat(t *testing.T) {
	body := []byte(`{"model":"x"}`)
	out := injectMemoryInstruction(body, "unknown")
	if !bytes.Equal(out, body) {
		t.Error("unknown format should return body unchanged")
	}
}

func TestInject_InvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	out := injectMemoryInstruction(body, "anthropic")
	if !bytes.Equal(out, body) {
		t.Error("invalid JSON should return body unchanged")
	}
}

// ── user reminder injection tests ─────────────────────────────────────────────
// These call the underlying functions directly to avoid a dependency on the
// ENABLE_SYSTEM_REMINDER environment variable.

func TestInjectUserReminder_Anthropic_StringContent(t *testing.T) {
	body := `{"model":"claude","messages":[{"role":"user","content":"What is 2+2?"}]}`
	out := injectAnthropicUserReminder([]byte(body))

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)
	var content string
	_ = json.Unmarshal(messages[len(messages)-1]["content"], &content)
	if !bytes.HasPrefix([]byte(content), []byte("<system-reminder>")) {
		t.Errorf("user message should start with reminder, got: %q", content)
	}
	if !bytes.Contains([]byte(content), []byte("What is 2+2?")) {
		t.Errorf("original content should be preserved, got: %q", content)
	}
}

func TestInjectUserReminder_Anthropic_ArrayContent(t *testing.T) {
	body := `{"model":"claude","messages":[{"role":"user","content":[{"type":"text","text":"Hello"}]}]}`
	out := injectAnthropicUserReminder([]byte(body))

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)
	var blocks []map[string]json.RawMessage
	_ = json.Unmarshal(messages[len(messages)-1]["content"], &blocks)
	if len(blocks) < 2 {
		t.Fatalf("expected at least 2 blocks (reminder + original), got %d", len(blocks))
	}
	var firstText string
	_ = json.Unmarshal(blocks[0]["text"], &firstText)
	if !bytes.HasPrefix([]byte(firstText), []byte("<system-reminder>")) {
		t.Errorf("first block should be the reminder, got: %q", firstText)
	}
	var secondText string
	_ = json.Unmarshal(blocks[1]["text"], &secondText)
	if secondText != "Hello" {
		t.Errorf("second block should preserve original text, got: %q", secondText)
	}
}

func TestInjectUserReminder_Anthropic_LastUserMessage(t *testing.T) {
	body := `{"model":"claude","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}]}`
	out := injectAnthropicUserReminder([]byte(body))

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)

	var firstContent string
	_ = json.Unmarshal(messages[0]["content"], &firstContent)
	if bytes.Contains([]byte(firstContent), []byte("<system-reminder>")) {
		t.Errorf("first user message should not have reminder, got: %q", firstContent)
	}
	var lastContent string
	_ = json.Unmarshal(messages[len(messages)-1]["content"], &lastContent)
	if !bytes.HasPrefix([]byte(lastContent), []byte("<system-reminder>")) {
		t.Errorf("last user message should have reminder, got: %q", lastContent)
	}
}

func TestInjectUserReminder_OpenAI(t *testing.T) {
	body := `{"model":"gpt","messages":[{"role":"system","content":"You help."},{"role":"user","content":"hi"}]}`
	out := injectOpenAIUserReminder([]byte(body))

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)
	var content string
	_ = json.Unmarshal(messages[len(messages)-1]["content"], &content)
	if !bytes.HasPrefix([]byte(content), []byte("<system-reminder>")) {
		t.Errorf("user message should start with reminder, got: %q", content)
	}
	if !bytes.Contains([]byte(content), []byte("hi")) {
		t.Errorf("original content should be preserved, got: %q", content)
	}
}
