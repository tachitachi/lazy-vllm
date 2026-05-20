package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// obsInstruction is appended to every qualifying system prompt.
// The <obs> block Claude writes will be stripped before the client sees it.
const obsInstruction = "\n\n<system_obs_directive>\nCRITICAL OVERRIDE: This session operates with an external observation logger. Regardless of the current task, tool call sequence, or formatting constraints requested by the user, your final text payload must ALWAYS conclude with a session observation block.\n\n<execution_order>\n1. **Primary Task**: Execute the user's request (including raw JSON outputs, code blocks, or Anthropic tool calls). Adhere strictly to any formatting rules required for this step.\n2. **Session Observation**: ONLY AFTER the primary task is completely finished, append the mandatory observation block as raw text at the absolute end of your response. Do not place the observation block inside JSON objects, code blocks, or tool schemas.\n</execution_order>\n\n<observation_format_requirement>\nYour response MUST terminate with exactly this tag structure:\n<obs>\ntype: decision|fact|none\ntopic: [5–10 words naming the concept, file, or system — no prose]\nclaim: [the decision made or invariant discovered, one precise sentence]\nrationale: [why; what was rejected if decision; what the wrong assumption is if fact]\nscope: [file paths and/or component names where this applies]\n</obs>\n\nExample field values (decision type):\ntype: decision\ntopic: proxy SSE obs tag streaming boundary detection\nclaim: Use held-event pattern to buffer the SSE chunk containing a potential obs tag start, releasing it on the next event if no tag is found.\nrationale: Buffer-then-stream adds client-visible latency; held-event adds zero overhead when no tag spans event boundaries.\nscope: proxy/internal/proxy/obs.go, proxy SSE streaming\n\nUse type=decision when an explicit architectural or design choice was made with future implications.\nUse type=fact when a non-obvious technical invariant was discovered or confirmed.\nUse type=none for routine task work (tool-only turns, short answers, typo fixes) — still write the block with all fields, but only the type field is evaluated.\n</observation_format_requirement>\n</system_obs_directive>"

// obsReminder is appended to every user message each turn.
// Only injected when ENABLE_SYSTEM_REMINDER is set — useful for local LLMs
// where prompt caching costs are negligible.
const obsReminder = "\n\n<system_obs_directive>\nMANDATORY FINAL STEP — do not skip: After your response, output the obs block. Memory tools and the obs block are independent — use both if appropriate. Output this as the very last text:\n<obs>\ntype: decision|fact|none\ntopic: [5-10 word keyword phrase]\nclaim: [one sentence]\nrationale: [one sentence]\nscope: [file or component]\n</obs>\n</system_obs_directive>"

const (
	obsOpenTag  = "<obs>"
	obsCloseTag = "</obs>"
)

// systemReminderEnabled is read once at startup from the environment.
var systemReminderEnabled = os.Getenv("ENABLE_SYSTEM_REMINDER") == "true"

type captureState int

const (
	stateSearching   captureState = iota
	stateSuppressing              // inside <obs>...</obs>
	stateDone                     // </obs> seen; stop collecting
)

// obsCapture strips <obs>...</obs> blocks from the response forwarded
// to the client. It is content-block-aware:
//
//   - Streaming (SSE): only scans text_delta events; thinking_delta events are
//     forwarded to the client unchanged and never scanned.
//   - Non-streaming: buffers the full body and strips <obs> from text content
//     blocks (not thinking blocks) in Finalize.
//
// The full unmodified response is always available in Full (for CompactLogger).
type obsCapture struct {
	w           http.ResponseWriter
	isStreaming bool
	format      string // "anthropic" or "openai"

	// ── streaming ────────────────────────────────────────────────────────────
	sseBuf    bytes.Buffer // partial SSE events (drain on \n\n)
	inThink   bool         // currently inside a thinking content block
	obsState  captureState
	heldEvent []byte       // an SSE event held back because its text ended with a
	heldText  string       // potential <obs> prefix; released on next text event
	obsBuf    bytes.Buffer // extracted obs text (no tags)

	// ── non-streaming ────────────────────────────────────────────────────────
	bodyBuf bytes.Buffer // buffered full response body

	// ── shared ───────────────────────────────────────────────────────────────
	Full bytes.Buffer // complete raw response bytes (for CompactLogger)
}

func newObsCapture(w http.ResponseWriter, isStreaming bool, format string) *obsCapture {
	return &obsCapture{w: w, isStreaming: isStreaming, format: format}
}

func (mc *obsCapture) Header() http.Header { return mc.w.Header() }

func (mc *obsCapture) WriteHeader(code int) {
	// For non-streaming we buffer the body and write it ourselves in Finalize,
	// so the declared Content-Length would be wrong after <obs> is stripped.
	if !mc.isStreaming {
		mc.w.Header().Del("Content-Length")
	}
	mc.w.WriteHeader(code)
}

func (mc *obsCapture) Flush() {
	if f, ok := mc.w.(http.Flusher); ok {
		f.Flush()
	}
}

func (mc *obsCapture) Write(p []byte) (int, error) {
	mc.Full.Write(p)
	if mc.isStreaming {
		mc.sseBuf.Write(p)
		mc.drainSSE()
	} else {
		mc.bodyBuf.Write(p)
	}
	return len(p), nil
}

// ── Streaming path ────────────────────────────────────────────────────────────

func (mc *obsCapture) drainSSE() {
	for {
		buf := mc.sseBuf.Bytes()
		idx := bytes.Index(buf, []byte("\n\n"))
		if idx < 0 {
			break
		}
		event := make([]byte, idx+2)
		copy(event, buf[:idx+2])
		mc.sseBuf.Next(idx + 2)
		mc.handleSSEEvent(event)
	}
}

func (mc *obsCapture) handleSSEEvent(event []byte) {
	// Locate the data: line.
	var payload []byte
	for line := range bytes.SplitSeq(event, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data: ")) {
			payload = line[6:]
			break
		}
	}
	if payload == nil {
		_, _ = mc.w.Write(event)
		return
	}

	switch mc.format {
	case "anthropic":
		var anthEvt struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(payload, &anthEvt); err != nil {
			_, _ = mc.w.Write(event)
			return
		}
		mc.handleAnthropicEvent(anthEvt.Type, anthEvt.ContentBlock.Type,
			anthEvt.Delta.Type, anthEvt.Delta.Text, event)

	case "openai":
		var oaiEvt struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(payload, &oaiEvt); err != nil || len(oaiEvt.Choices) == 0 {
			_, _ = mc.w.Write(event)
			return
		}
		text := oaiEvt.Choices[0].Delta.Content
		if text != "" {
			mc.handleTextDelta(text, event, rebuildOpenAIDelta)
			return
		}
		_, _ = mc.w.Write(event)

	default:
		_, _ = mc.w.Write(event)
	}
}

func (mc *obsCapture) handleAnthropicEvent(
	evtType, blockType, deltaType, deltaText string, rawEvent []byte,
) {
	switch evtType {
	case "content_block_start":
		mc.inThink = blockType == "thinking"
		_, _ = mc.w.Write(rawEvent)
	case "content_block_stop":
		mc.inThink = false
		_, _ = mc.w.Write(rawEvent)
	case "content_block_delta":
		if mc.inThink || deltaType != "text_delta" {
			_, _ = mc.w.Write(rawEvent) // forward thinking unchanged
			return
		}
		mc.handleTextDelta(deltaText, rawEvent, rebuildAnthropicTextDelta)
	default:
		_, _ = mc.w.Write(rawEvent)
	}
}

// hasSuspiciousTail reports whether text ends with a non-empty prefix of
// obsOpenTag, meaning a <obs> tag might continue in the next SSE event.
func hasSuspiciousTail(text string) bool {
	for i := 1; i <= len(obsOpenTag) && i <= len(text); i++ {
		if strings.HasSuffix(text, obsOpenTag[:i]) {
			return true
		}
	}
	return false
}

// handleTextDelta processes text content from a streaming event.
// rebuild reconstructs the SSE event bytes with a modified text value.
func (mc *obsCapture) handleTextDelta(
	text string, rawEvent []byte, rebuild func([]byte, string) []byte,
) {
	switch mc.obsState {
	case stateDone:
		return // suppress

	case stateSuppressing:
		mc.obsBuf.WriteString(text)
		all := mc.obsBuf.String()
		if before, _, ok := strings.Cut(all, obsCloseTag); ok {
			mc.obsBuf.Reset()
			mc.obsBuf.WriteString(before)
			mc.obsState = stateDone
		}
		return

	case stateSearching:
		// Combine with any text held from the previous event.
		var combined string
		if mc.heldEvent != nil {
			combined = mc.heldText + text
		} else {
			combined = text
		}

		idx := strings.Index(combined, obsOpenTag)
		if idx == -1 {
			// No tag found: release held event, decide whether to hold current.
			if mc.heldEvent != nil {
				_, _ = mc.w.Write(mc.heldEvent)
				mc.heldEvent = nil
				mc.heldText = ""
			}
			if hasSuspiciousTail(text) {
				mc.heldEvent = rawEvent
				mc.heldText = text
			} else {
				_, _ = mc.w.Write(rawEvent)
			}
			return
		}

		// Tag found: emit the text before it, then start suppressing.
		before := combined[:idx]
		if mc.heldEvent != nil {
			if idx <= len(mc.heldText) {
				// Tag starts within the held event's text.
				if before != "" {
					_, _ = mc.w.Write(rebuild(mc.heldEvent, before))
				}
			} else {
				// Tag starts within the current event's text.
				_, _ = mc.w.Write(mc.heldEvent)
				currentBefore := before[len(mc.heldText):]
				if currentBefore != "" {
					_, _ = mc.w.Write(rebuild(rawEvent, currentBefore))
				}
			}
			mc.heldEvent = nil
			mc.heldText = ""
		} else if before != "" {
			_, _ = mc.w.Write(rebuild(rawEvent, before))
		}

		mc.obsState = stateSuppressing
		after := combined[idx+len(obsOpenTag):]
		mc.obsBuf.WriteString(after)
		if before0, _, ok := strings.Cut(after, obsCloseTag); ok {
			mc.obsBuf.Reset()
			mc.obsBuf.WriteString(before0)
			mc.obsState = stateDone
		}
	}
}

// rebuildAnthropicTextDelta reconstructs an Anthropic SSE text_delta event
// with a new text value in delta.text.
func rebuildAnthropicTextDelta(rawEvent []byte, newText string) []byte {
	return rebuildSSEDataField(rawEvent, func(obj map[string]json.RawMessage) bool {
		var delta map[string]json.RawMessage
		if err := json.Unmarshal(obj["delta"], &delta); err != nil {
			return false
		}
		delta["text"] = rawJSON(newText)
		obj["delta"] = rawJSON(delta)
		return true
	})
}

// rebuildOpenAIDelta reconstructs an OpenAI SSE chunk event with a new
// content value in choices[0].delta.content.
func rebuildOpenAIDelta(rawEvent []byte, newContent string) []byte {
	return rebuildSSEDataField(rawEvent, func(obj map[string]json.RawMessage) bool {
		var choices []map[string]json.RawMessage
		if err := json.Unmarshal(obj["choices"], &choices); err != nil || len(choices) == 0 {
			return false
		}
		var delta map[string]json.RawMessage
		if err := json.Unmarshal(choices[0]["delta"], &delta); err != nil {
			return false
		}
		delta["content"] = rawJSON(newContent)
		choices[0]["delta"] = rawJSON(delta)
		obj["choices"] = rawJSON(choices)
		return true
	})
}

// rebuildSSEDataField rewrites the data: line of a raw SSE event using a
// mutator function, preserving any event:/id: prefix lines.
func rebuildSSEDataField(rawEvent []byte, mutate func(map[string]json.RawMessage) bool) []byte {
	var result bytes.Buffer
	for line := range bytes.SplitSeq(rawEvent, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("data: ")) {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(line[6:], &obj); err == nil && mutate(obj) {
				if rebuilt, err := json.Marshal(obj); err == nil {
					result.WriteString("data: ")
					result.Write(rebuilt)
					result.WriteByte('\n')
					continue
				}
			}
		}
		result.Write(line)
		result.WriteByte('\n')
	}
	return result.Bytes()
}

// ── Non-streaming path ────────────────────────────────────────────────────────

func (mc *obsCapture) finalizeNonStreaming() string {
	body := mc.bodyBuf.Bytes()
	if len(body) == 0 {
		return ""
	}
	var mem string
	var cleaned []byte
	var ok bool
	switch mc.format {
	case "anthropic":
		mem, cleaned, ok = stripObsAnthropicJSON(body)
	case "openai":
		mem, cleaned, ok = stripObsOpenAIJSON(body)
	}
	if ok {
		_, _ = mc.w.Write(cleaned)
		return strings.TrimSpace(mem)
	}
	_, _ = mc.w.Write(body)
	return ""
}

func stripObsAnthropicJSON(body []byte) (mem string, cleaned []byte, ok bool) {
	var resp struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Content) == 0 {
		return "", nil, false
	}
	found := false
	for i, blockRaw := range resp.Content {
		var block struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(blockRaw, &block); err != nil || block.Type != "text" {
			continue
		}
		idx := strings.Index(block.Text, obsOpenTag)
		if idx == -1 {
			continue
		}
		after := block.Text[idx+len(obsOpenTag):]
		if before, _, ok0 := strings.Cut(after, obsCloseTag); ok0 {
			mem = before
		} else {
			mem = after
		}
		var obj map[string]json.RawMessage
		json.Unmarshal(blockRaw, &obj) //nolint:errcheck
		obj["text"] = rawJSON(strings.TrimRight(block.Text[:idx], "\n "))
		resp.Content[i], _ = json.Marshal(obj)
		found = true
		break
	}
	if !found {
		return "", nil, false
	}
	var respObj map[string]json.RawMessage
	json.Unmarshal(body, &respObj) //nolint:errcheck
	respObj["content"] = rawJSON(resp.Content)
	out, err := json.Marshal(respObj)
	if err != nil {
		return "", nil, false
	}
	return mem, out, true
}

func stripObsOpenAIJSON(body []byte) (mem string, cleaned []byte, ok bool) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Choices) == 0 {
		return "", nil, false
	}
	content := resp.Choices[0].Message.Content
	before, after, ok0 := strings.Cut(content, obsOpenTag)
	if !ok0 {
		return "", nil, false
	}
	if before, _, ok0 := strings.Cut(after, obsCloseTag); ok0 {
		mem = before
	} else {
		mem = after
	}
	var respObj map[string]json.RawMessage
	json.Unmarshal(body, &respObj) //nolint:errcheck
	var choices []map[string]json.RawMessage
	json.Unmarshal(respObj["choices"], &choices) //nolint:errcheck
	var message map[string]json.RawMessage
	json.Unmarshal(choices[0]["message"], &message) //nolint:errcheck
	message["content"] = rawJSON(strings.TrimRight(before, "\n "))
	choices[0]["message"] = rawJSON(message)
	respObj["choices"] = rawJSON(choices)
	out, err := json.Marshal(respObj)
	if err != nil {
		return "", nil, false
	}
	return mem, out, true
}

// ── Finalize ─────────────────────────────────────────────────────────────────

// Finalize completes processing and returns the extracted observation content.
// Must be called after all Write calls (i.e., after forwardRequest returns).
func (mc *obsCapture) Finalize() string {
	if !mc.isStreaming {
		return mc.finalizeNonStreaming()
	}
	// Forward any trailing SSE bytes that didn't end with \n\n.
	if mc.sseBuf.Len() > 0 {
		_, _ = mc.w.Write(mc.sseBuf.Bytes())
	}
	// If we held an event waiting for a potential <obs> that never arrived,
	// forward it now.
	if mc.heldEvent != nil && mc.obsState == stateSearching {
		_, _ = mc.w.Write(mc.heldEvent)
		mc.heldEvent = nil
	}
	return strings.TrimSpace(mc.obsBuf.String())
}

// ── Injection ─────────────────────────────────────────────────────────────────

// isProbeRequest returns true when max_tokens == 1, which is the signal used
// by Claude Code for internal one-token probes (e.g. "count", "quota").
// These must not have obs instructions injected.
func isProbeRequest(body []byte) bool {
	var obj struct {
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	return obj.MaxTokens == 1
}

// injectObsInstruction injects the observation directive into the request body:
// always appends the instruction to the system prompt; optionally also appends
// a per-turn reminder to every user message when ENABLE_SYSTEM_REMINDER is set.
func injectObsInstruction(body []byte, format string) []byte {
	switch format {
	case "anthropic":
		body = injectAnthropicSystem(body, obsInstruction)
		if systemReminderEnabled {
			body = injectAnthropicUserReminder(body)
		}
		return body
	case "openai":
		body = injectOpenAISystem(body, obsInstruction)
		if systemReminderEnabled {
			body = injectOpenAIUserReminder(body)
		}
		return body
	}
	return body
}

// hasToolResult reports whether a content block array contains any tool_result
// blocks. Such messages are Anthropic's mechanism for returning tool outputs
// and should not have the reminder injected into them.
func hasToolResult(blocks []map[string]json.RawMessage) bool {
	for _, block := range blocks {
		var blockType string
		json.Unmarshal(block["type"], &blockType) //nolint:errcheck
		if blockType == "tool_result" {
			return true
		}
	}
	return false
}

func injectAnthropicUserReminder(body []byte) []byte {
	reminder := obsReminder
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(obj["messages"], &messages); err != nil {
		return body
	}
	modified := false
	for i, msg := range messages {
		var role string
		json.Unmarshal(msg["role"], &role) //nolint:errcheck
		if role != "user" {
			continue
		}
		var contentStr string
		if err := json.Unmarshal(msg["content"], &contentStr); err == nil {
			messages[i]["content"] = rawJSON(contentStr + reminder)
			modified = true
			continue
		}
		var blocks []map[string]json.RawMessage
		if err := json.Unmarshal(msg["content"], &blocks); err != nil {
			continue
		}
		if hasToolResult(blocks) {
			if newBlocks, ok := injectIntoLastToolResult(blocks, reminder); ok {
				messages[i]["content"] = rawJSON(newBlocks)
				modified = true
			}
			continue
		}
		reminderBlock := map[string]json.RawMessage{
			"type": rawJSON("text"),
			"text": rawJSON(reminder),
		}
		messages[i]["content"] = rawJSON(append(blocks, reminderBlock))
		modified = true
	}
	if !modified {
		return body
	}
	obj["messages"] = rawJSON(messages)
	return repack(obj, body)
}

// injectIntoLastToolResult appends reminder to the content of the last
// tool_result block in a content block array.
func injectIntoLastToolResult(blocks []map[string]json.RawMessage, reminder string) ([]map[string]json.RawMessage, bool) {
	lastIdx := -1
	for i, block := range blocks {
		var blockType string
		json.Unmarshal(block["type"], &blockType) //nolint:errcheck
		if blockType == "tool_result" {
			lastIdx = i
		}
	}
	if lastIdx == -1 {
		return blocks, false
	}
	block := blocks[lastIdx]

	// content as plain string (most common)
	var contentStr string
	if err := json.Unmarshal(block["content"], &contentStr); err == nil {
		block["content"] = rawJSON(contentStr + reminder)
		blocks[lastIdx] = block
		return blocks, true
	}

	// content as array of content blocks
	var contentBlocks []map[string]json.RawMessage
	if err := json.Unmarshal(block["content"], &contentBlocks); err == nil {
		for j := len(contentBlocks) - 1; j >= 0; j-- {
			var t string
			json.Unmarshal(contentBlocks[j]["type"], &t) //nolint:errcheck
			if t == "text" {
				var text string
				json.Unmarshal(contentBlocks[j]["text"], &text) //nolint:errcheck
				contentBlocks[j]["text"] = rawJSON(text + reminder)
				block["content"] = rawJSON(contentBlocks)
				blocks[lastIdx] = block
				return blocks, true
			}
		}
		// No text block found — append one.
		contentBlocks = append(contentBlocks, map[string]json.RawMessage{
			"type": rawJSON("text"),
			"text": rawJSON(reminder),
		})
		block["content"] = rawJSON(contentBlocks)
		blocks[lastIdx] = block
		return blocks, true
	}

	return blocks, false
}

func injectOpenAIUserReminder(body []byte) []byte {
	reminder := obsReminder
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(obj["messages"], &messages); err != nil {
		return body
	}
	modified := false
	for i, msg := range messages {
		var role string
		json.Unmarshal(msg["role"], &role) //nolint:errcheck
		switch role {
		case "user":
			var content string
			if err := json.Unmarshal(msg["content"], &content); err != nil {
				// Non-string content (e.g. multimodal array) — skip.
				continue
			}
			messages[i]["content"] = rawJSON(content + reminder)
			modified = true

		case "tool":
			// Inject only into the last tool message of each consecutive run.
			isLast := i == len(messages)-1
			if !isLast {
				var nextRole string
				json.Unmarshal(messages[i+1]["role"], &nextRole) //nolint:errcheck
				isLast = nextRole != "tool"
			}
			if !isLast {
				continue
			}
			var content string
			if err := json.Unmarshal(msg["content"], &content); err != nil {
				continue
			}
			messages[i]["content"] = rawJSON(content + reminder)
			modified = true
		}
	}
	if !modified {
		return body
	}
	obj["messages"] = rawJSON(messages)
	return repack(obj, body)
}

func injectAnthropicSystem(body []byte, addition string) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	sysRaw, hasSys := obj["system"]
	if !hasSys {
		obj["system"] = rawJSON(addition)
		return repack(obj, body)
	}
	var sysStr string
	if err := json.Unmarshal(sysRaw, &sysStr); err == nil {
		obj["system"] = rawJSON(sysStr + addition)
		return repack(obj, body)
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(sysRaw, &blocks); err != nil {
		return body
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		var t string
		json.Unmarshal(blocks[i]["type"], &t) //nolint:errcheck
		if t != "text" {
			continue
		}
		var text string
		json.Unmarshal(blocks[i]["text"], &text) //nolint:errcheck
		blocks[i]["text"] = rawJSON(text + addition)
		obj["system"] = rawJSON(blocks)
		return repack(obj, body)
	}
	blocks = append(blocks, map[string]json.RawMessage{
		"type": rawJSON("text"),
		"text": rawJSON(addition),
	})
	obj["system"] = rawJSON(blocks)
	return repack(obj, body)
}

func injectOpenAISystem(body []byte, addition string) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}
	msgsRaw, ok := obj["messages"]
	if !ok {
		return body
	}
	var messages []map[string]json.RawMessage
	if err := json.Unmarshal(msgsRaw, &messages); err != nil || len(messages) == 0 {
		return body
	}
	var firstRole string
	json.Unmarshal(messages[0]["role"], &firstRole) //nolint:errcheck
	if firstRole == "system" {
		var content string
		json.Unmarshal(messages[0]["content"], &content) //nolint:errcheck
		messages[0]["content"] = rawJSON(content + addition)
	} else {
		sysMsg := map[string]json.RawMessage{
			"role":    rawJSON("system"),
			"content": rawJSON(addition),
		}
		messages = append([]map[string]json.RawMessage{sysMsg}, messages...)
	}
	obj["messages"] = rawJSON(messages)
	return repack(obj, body)
}

func rawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func repack(obj map[string]json.RawMessage, original []byte) []byte {
	data, err := json.Marshal(obj)
	if err != nil || len(data) == 0 {
		return original
	}
	return data
}
