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

func TestObsCapture_NonStreaming_NoTag(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, false, "anthropic")
	mc.WriteHeader(200)

	body := `{"content":[{"type":"text","text":"hello world"}]}`
	_, _ = mc.Write([]byte(body))
	got := mc.Finalize()

	if got != "" {
		t.Errorf("expected empty observation, got %q", got)
	}
	// Client should receive the full body.
	if !bytes.Contains(rr.Body.Bytes(), []byte("hello world")) {
		t.Errorf("client body missing text: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_NonStreaming_AnthropicTextBlock(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, false, "anthropic")
	mc.WriteHeader(200)

	body := `{"content":[{"type":"text","text":"The answer is 4.\n<obs>User asked 2+2, answered 4.</obs>"}]}`
	_, _ = mc.Write([]byte(body))
	got := mc.Finalize()

	if got != "User asked 2+2, answered 4." {
		t.Errorf("obs content: got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("<obs>")) {
		t.Errorf("client body should not contain <obs>: %q", rr.Body.Bytes())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("The answer is 4.")) {
		t.Errorf("client body should contain response text: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_NonStreaming_AnthropicIgnoresThinkingBlock(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, false, "anthropic")
	mc.WriteHeader(200)

	// thinking block contains <obs>; text block also has one
	body := `{"content":[{"type":"thinking","thinking":"I will write <obs>fake</obs>"},{"type":"text","text":"Real answer.\n<obs>real observation</obs>"}]}`
	_, _ = mc.Write([]byte(body))
	got := mc.Finalize()

	if got != "real observation" {
		t.Errorf("should capture from text block, got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("real observation")) {
		t.Errorf("client body should not contain obs content: %q", rr.Body.Bytes())
	}
	// Thinking block content should be untouched in client body.
	if !bytes.Contains(rr.Body.Bytes(), []byte("fake")) {
		t.Errorf("thinking block should be preserved in client body: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_NonStreaming_OpenAIIgnoresReasoning(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, false, "openai")
	mc.WriteHeader(200)

	body := `{"choices":[{"message":{"role":"assistant","content":"Answer.\n<obs>openai obs</obs>","reasoning":"I think <obs>fake</obs>"}}]}`
	_, _ = mc.Write([]byte(body))
	got := mc.Finalize()

	if got != "openai obs" {
		t.Errorf("should capture from content field, got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("openai obs")) {
		t.Errorf("obs should be stripped from client body: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_NonStreaming_FullBufferIntact(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, false, "anthropic")
	mc.WriteHeader(200)

	raw := `{"content":[{"type":"text","text":"hi<obs>obs</obs>"}]}`
	_, _ = mc.Write([]byte(raw))
	mc.Finalize()

	if mc.Full.String() != raw {
		t.Errorf("Full buffer should contain original bytes, got %q", mc.Full.String())
	}
}

// ── streaming tests ───────────────────────────────────────────────────────────

func TestObsCapture_Streaming_NoTag(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, true, "anthropic")

	_, _ = mc.Write(sseEvent("text_delta", "hello"))
	_, _ = mc.Write(sseEvent("text_delta", " world"))
	got := mc.Finalize()

	if got != "" {
		t.Errorf("expected empty observation, got %q", got)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("hello")) {
		t.Errorf("client body missing text: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_Streaming_TextDeltaWithTag(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, true, "anthropic")

	_, _ = mc.Write(sseEvent("text_delta", "Response here."))
	_, _ = mc.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"\\n<obs>did the thing</obs>\"}}\n\n"))
	got := mc.Finalize()

	if got != "did the thing" {
		t.Errorf("obs content: got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("did the thing")) {
		t.Errorf("client body should not contain obs: %q", rr.Body.Bytes())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("Response here.")) {
		t.Errorf("client body should contain response text: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_Streaming_ThinkingDeltaNotScanned(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, true, "anthropic")

	// thinking block fires first with <obs> — should be ignored
	_, _ = mc.Write(sseBlockStart("thinking"))
	_, _ = mc.Write(sseEvent("thinking_delta", "I plan to write <obs>fake</obs>"))
	_, _ = mc.Write(sseBlockStop())

	// text block fires second with the real <obs>
	_, _ = mc.Write(sseBlockStart("text"))
	_, _ = mc.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Real answer.\\n<obs>real obs</obs>\"}}\n\n"))
	_, _ = mc.Write(sseBlockStop())

	got := mc.Finalize()

	if got != "real obs" {
		t.Errorf("should capture from text_delta only, got %q", got)
	}
	// Client should see the thinking block content untouched.
	if !bytes.Contains(rr.Body.Bytes(), []byte("fake")) {
		t.Errorf("thinking block should reach client: %q", rr.Body.Bytes())
	}
	// Client should NOT see the obs block.
	if bytes.Contains(rr.Body.Bytes(), []byte("real obs")) {
		t.Errorf("obs should not reach client: %q", rr.Body.Bytes())
	}
	// Client should see the text before <obs>.
	if !bytes.Contains(rr.Body.Bytes(), []byte("Real answer.")) {
		t.Errorf("response text should reach client: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_Streaming_ThinkingDeltaForwardedUnchanged(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, true, "anthropic")

	// Thinking block with no <obs> — should be forwarded verbatim.
	_, _ = mc.Write(sseBlockStart("thinking"))
	raw := sseEvent("thinking_delta", "normal thinking text")
	_, _ = mc.Write(raw)
	_, _ = mc.Write(sseBlockStop())
	mc.Finalize()

	if !bytes.Contains(rr.Body.Bytes(), []byte("normal thinking text")) {
		t.Errorf("thinking text should be forwarded: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_Streaming_CrossEventBoundary(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, true, "anthropic")

	// Tag split across two events: "before<ob" + "s>summary_obs</obs>"
	_, _ = mc.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"before<ob\"}}\n\n"))
	_, _ = mc.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"s>summary_obs</obs>\"}}\n\n"))
	got := mc.Finalize()

	if got != "summary_obs" {
		t.Errorf("obs content: got %q", got)
	}
	if bytes.Contains(rr.Body.Bytes(), []byte("summary_obs")) {
		t.Errorf("obs should not reach client: %q", rr.Body.Bytes())
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte("before")) {
		t.Errorf("pre-tag text should reach client: %q", rr.Body.Bytes())
	}
}

func TestObsCapture_Streaming_FullBufferIntact(t *testing.T) {
	rr := httptest.NewRecorder()
	mc := newObsCapture(rr, true, "anthropic")

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
	out := injectObsInstruction([]byte(body), "anthropic")

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var sys string
	_ = json.Unmarshal(obj["system"], &sys)
	if !bytes.Contains([]byte(sys), []byte("<obs>")) {
		t.Errorf("system prompt should contain obs instruction, got: %q", sys)
	}
	if !bytes.HasPrefix([]byte(sys), []byte("Be helpful.")) {
		t.Errorf("existing system text should be preserved, got: %q", sys)
	}
}

func TestInject_Anthropic_NoSystem(t *testing.T) {
	body := `{"model":"claude","messages":[]}`
	out := injectObsInstruction([]byte(body), "anthropic")

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
	out := injectObsInstruction([]byte(body), "anthropic")

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
	if !bytes.Contains([]byte(text), []byte("<obs>")) {
		t.Errorf("instruction should be appended, got: %q", text)
	}
}

func TestInject_OpenAI_WithSystem(t *testing.T) {
	body := `{"model":"gpt","messages":[{"role":"system","content":"You help."},{"role":"user","content":"hi"}]}`
	out := injectObsInstruction([]byte(body), "openai")

	var obj map[string]json.RawMessage
	_ = json.Unmarshal(out, &obj)
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)

	var content string
	_ = json.Unmarshal(messages[0]["content"], &content)
	if !bytes.Contains([]byte(content), []byte("You help.")) {
		t.Errorf("original system content lost, got: %q", content)
	}
	if !bytes.Contains([]byte(content), []byte("<obs>")) {
		t.Errorf("instruction not appended, got: %q", content)
	}
}

func TestInject_OpenAI_WithoutSystem(t *testing.T) {
	body := `{"model":"gpt","messages":[{"role":"user","content":"hi"}]}`
	out := injectObsInstruction([]byte(body), "openai")

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
	out := injectObsInstruction(body, "unknown")
	if !bytes.Equal(out, body) {
		t.Error("unknown format should return body unchanged")
	}
}

func TestInject_InvalidJSON(t *testing.T) {
	body := []byte(`not json`)
	out := injectObsInstruction(body, "anthropic")
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
	if !bytes.Contains([]byte(content), []byte("What is 2+2?")) {
		t.Errorf("original content should be preserved, got: %q", content)
	}
	if !bytes.HasSuffix([]byte(content), []byte("</system_obs_directive>")) {
		t.Errorf("user message should end with reminder, got: %q", content)
	}
	if !bytes.HasPrefix([]byte(content), []byte("What is 2+2?")) {
		t.Errorf("original content should come before reminder, got: %q", content)
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
		t.Fatalf("expected at least 2 blocks (original + reminder), got %d", len(blocks))
	}
	var firstText string
	_ = json.Unmarshal(blocks[0]["text"], &firstText)
	if firstText != "Hello" {
		t.Errorf("first block should preserve original text, got: %q", firstText)
	}
	var lastText string
	_ = json.Unmarshal(blocks[len(blocks)-1]["text"], &lastText)
	if !bytes.HasSuffix([]byte(lastText), []byte("</system_obs_directive>")) {
		t.Errorf("last block should be the reminder, got: %q", lastText)
	}
}

func TestInjectUserReminder_Anthropic_AllUserMessages(t *testing.T) {
	body := `{"model":"claude","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"second"}]}`
	out := injectAnthropicUserReminder([]byte(body))

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)

	// Every user message should have the reminder; assistant messages should not.
	for _, msg := range messages {
		var role, content string
		_ = json.Unmarshal(msg["role"], &role)
		_ = json.Unmarshal(msg["content"], &content)
		if role == "user" {
			if !bytes.HasSuffix([]byte(content), []byte("</system_obs_directive>")) {
				t.Errorf("user message should end with reminder, got: %q", content)
			}
		} else {
			if bytes.Contains([]byte(content), []byte("system_obs_directive")) {
				t.Errorf("non-user message should not have reminder, got: %q", content)
			}
		}
	}
}

func TestInjectUserReminder_Anthropic_InjectsIntoLastToolResult(t *testing.T) {
	// The reminder is injected into the content of the last tool_result block.
	body := `{"model":"claude","messages":[` +
		`{"role":"user","content":"do X"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"1","name":"bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"1","content":"grep output"}]}` +
		`]}`
	out := injectAnthropicUserReminder([]byte(body))

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)

	// First user message (text) should have the reminder appended.
	var firstContent string
	_ = json.Unmarshal(messages[0]["content"], &firstContent)
	if !bytes.HasSuffix([]byte(firstContent), []byte("</system_obs_directive>")) {
		t.Errorf("text user message should end with reminder, got: %q", firstContent)
	}

	// Last user message: still exactly 1 block (no new block added), but its
	// content should end with the reminder.
	var lastBlocks []map[string]json.RawMessage
	_ = json.Unmarshal(messages[len(messages)-1]["content"], &lastBlocks)
	if len(lastBlocks) != 1 {
		t.Fatalf("tool_result message should still have 1 block, got %d", len(lastBlocks))
	}
	var blockType string
	_ = json.Unmarshal(lastBlocks[0]["type"], &blockType)
	if blockType != "tool_result" {
		t.Errorf("block type should be tool_result, got %q", blockType)
	}
	var toolContent string
	_ = json.Unmarshal(lastBlocks[0]["content"], &toolContent)
	if !bytes.HasPrefix([]byte(toolContent), []byte("grep output")) {
		t.Errorf("tool result content should start with original output, got: %q", toolContent)
	}
	if !bytes.HasSuffix([]byte(toolContent), []byte("</system_obs_directive>")) {
		t.Errorf("tool result content should end with reminder, got: %q", toolContent)
	}
}

func TestInjectUserReminder_Anthropic_LastOfMultipleToolResults(t *testing.T) {
	// With 3 tool_result blocks, only the last one gets the reminder injected.
	body := `{"model":"claude","messages":[` +
		`{"role":"user","content":"do X"},` +
		`{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"1","name":"bash","input":{}},` +
		`{"type":"tool_use","id":"2","name":"grep","input":{}},` +
		`{"type":"tool_use","id":"3","name":"read","input":{}}` +
		`]},` +
		`{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"1","content":"bash out"},` +
		`{"type":"tool_result","tool_use_id":"2","content":"grep out"},` +
		`{"type":"tool_result","tool_use_id":"3","content":"read out"}` +
		`]}` +
		`]}`
	out := injectAnthropicUserReminder([]byte(body))

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)
	var blocks []map[string]json.RawMessage
	_ = json.Unmarshal(messages[len(messages)-1]["content"], &blocks)
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}

	// First two tool_results must be untouched.
	for _, idx := range []int{0, 1} {
		var content string
		_ = json.Unmarshal(blocks[idx]["content"], &content)
		if bytes.Contains([]byte(content), []byte("system_obs_directive")) {
			t.Errorf("block %d should not have reminder, got: %q", idx, content)
		}
	}

	// Third (last) tool_result should have reminder in its content.
	var lastContent string
	_ = json.Unmarshal(blocks[2]["content"], &lastContent)
	if !bytes.HasPrefix([]byte(lastContent), []byte("read out")) {
		t.Errorf("last tool result should start with original content, got: %q", lastContent)
	}
	if !bytes.HasSuffix([]byte(lastContent), []byte("</system_obs_directive>")) {
		t.Errorf("last tool result should end with reminder, got: %q", lastContent)
	}
}

func TestInjectUserReminder_OpenAI_ToolMessages(t *testing.T) {
	// Last tool message in each consecutive run gets the reminder.
	body := `{"model":"gpt","messages":[` +
		`{"role":"user","content":"do X"},` +
		`{"role":"assistant","content":"ok","tool_calls":[{"id":"1"},{"id":"2"}]},` +
		`{"role":"tool","tool_call_id":"1","content":"result1"},` +
		`{"role":"tool","tool_call_id":"2","content":"result2"},` +
		`{"role":"assistant","content":"more"},` +
		`{"role":"tool","tool_call_id":"3","content":"result3"}` +
		`]}`
	out := injectOpenAIUserReminder([]byte(body))

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(obj["messages"], &messages)

	getContent := func(i int) string {
		var c string
		_ = json.Unmarshal(messages[i]["content"], &c)
		return c
	}

	// result1 (index 2) — not last of first run, should be untouched.
	if bytes.Contains([]byte(getContent(2)), []byte("system_obs_directive")) {
		t.Errorf("result1 should not have reminder, got: %q", getContent(2))
	}
	// result2 (index 3) — last of first run, should have reminder.
	if !bytes.HasSuffix([]byte(getContent(3)), []byte("</system_obs_directive>")) {
		t.Errorf("result2 should end with reminder, got: %q", getContent(3))
	}
	// result3 (index 5) — only in second run, should have reminder.
	if !bytes.HasSuffix([]byte(getContent(5)), []byte("</system_obs_directive>")) {
		t.Errorf("result3 should end with reminder, got: %q", getContent(5))
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
	if !bytes.HasPrefix([]byte(content), []byte("hi")) {
		t.Errorf("original content should come before reminder, got: %q", content)
	}
	if !bytes.HasSuffix([]byte(content), []byte("</system_obs_directive>")) {
		t.Errorf("user message should end with reminder, got: %q", content)
	}
}
