package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- handleChatCompletions ---

func TestHandleChatCompletions_InjectsThinkingTrue_ForREASONING(t *testing.T) {
	var capturedMain []byte
	srv := httptest.NewServer(dualHandler("REASONING", vllmCompletionResponse(), &capturedMain))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"explain recursion"}]}`))
	req.Header.Set("Content-Type", "application/json")

	s.handleChatCompletions(httptest.NewRecorder(), req)

	assertEnableThinking(t, capturedMain, true)
}

func TestHandleChatCompletions_InjectsThinkingFalse_ForDIRECT(t *testing.T) {
	var capturedMain []byte
	srv := httptest.NewServer(dualHandler("DIRECT", vllmCompletionResponse(), &capturedMain))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")

	s.handleChatCompletions(httptest.NewRecorder(), req)

	assertEnableThinking(t, capturedMain, false)
}

func TestHandleChatCompletions_MessageWindowing_TruncatesToWindow(t *testing.T) {
	// 5-message conversation with WindowSize=3: classify should receive system + last 3 = 4 messages.
	var capturedClassify []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var top map[string]json.RawMessage
		json.Unmarshal(body, &top)
		if _, isClassify := top["structured_outputs"]; isClassify {
			capturedClassify = body
			w.Write(vllmClassifyResponse("DIRECT"))
		} else {
			w.Write(vllmCompletionResponse())
		}
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	msgs := []map[string]string{
		{"role": "user", "content": "msg1"},
		{"role": "assistant", "content": "msg2"},
		{"role": "user", "content": "msg3"},
		{"role": "assistant", "content": "msg4"},
		{"role": "user", "content": "msg5"},
	}
	body, _ := json.Marshal(map[string]any{"model": "test", "messages": msgs})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.handleChatCompletions(httptest.NewRecorder(), req)

	var payload map[string]json.RawMessage
	json.Unmarshal(capturedClassify, &payload)
	var classifyMsgs []ChatMessage
	json.Unmarshal(payload["messages"], &classifyMsgs)

	// system prompt + 3 windowed messages
	if len(classifyMsgs) != 4 {
		t.Fatalf("classify received %d messages, want 4 (system + 3 window)", len(classifyMsgs))
	}
	// Last windowed message should be msg5
	last, _ := classifyMsgs[3].Content.(string)
	if last != "msg5" {
		t.Errorf("last classify message content: got %q, want msg5", last)
	}
}

func TestHandleChatCompletions_MessageWindowing_SendsAllWhenUnderLimit(t *testing.T) {
	// 2 messages with WindowSize=3: classify should receive system + all 2 = 3 messages.
	var capturedClassify []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var top map[string]json.RawMessage
		json.Unmarshal(body, &top)
		if _, isClassify := top["structured_outputs"]; isClassify {
			capturedClassify = body
			w.Write(vllmClassifyResponse("DIRECT"))
		} else {
			w.Write(vllmCompletionResponse())
		}
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleChatCompletions(httptest.NewRecorder(), req)

	var payload map[string]json.RawMessage
	json.Unmarshal(capturedClassify, &payload)
	var classifyMsgs []ChatMessage
	json.Unmarshal(payload["messages"], &classifyMsgs)

	if len(classifyMsgs) != 3 {
		t.Errorf("classify received %d messages, want 3 (system + 2)", len(classifyMsgs))
	}
}

func TestHandleChatCompletions_InvalidJSON_Returns400(t *testing.T) {
	s := &server{cfg: testConfig("http://unused")}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("not json"))
	w := httptest.NewRecorder()
	s.handleChatCompletions(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestHandleChatCompletions_FailOpen_WhenClassifyErrors(t *testing.T) {
	// Classify returns 500; the main call should still proceed with enable_thinking=true.
	var capturedMain []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var top map[string]json.RawMessage
		json.Unmarshal(body, &top)
		if _, isClassify := top["structured_outputs"]; isClassify {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			capturedMain = body
			w.Write(vllmCompletionResponse())
		}
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleChatCompletions(httptest.NewRecorder(), req)

	assertEnableThinking(t, capturedMain, true)
}

func TestHandleChatCompletions_ForwardsAuthHeader_ToMainCall(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var top map[string]json.RawMessage
		json.Unmarshal(body, &top)
		if _, isClassify := top["structured_outputs"]; isClassify {
			w.Write(vllmClassifyResponse("DIRECT"))
		} else {
			capturedAuth = r.Header.Get("Authorization")
			w.Write(vllmCompletionResponse())
		}
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer my-token")
	s.handleChatCompletions(httptest.NewRecorder(), req)

	if capturedAuth != "Bearer my-token" {
		t.Errorf("Authorization: got %q, want %q", capturedAuth, "Bearer my-token")
	}
}

func TestHandleChatCompletions_StreamingResponse_PassedThrough(t *testing.T) {
	streamData := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
	srv := httptest.NewServer(dualHandler("DIRECT", []byte(streamData), nil))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handleChatCompletions(w, req)

	if w.Body.String() != streamData {
		t.Errorf("streaming body: got %q, want %q", w.Body.String(), streamData)
	}
}

func TestHandleChatCompletions_PreservesOriginalRequestFields(t *testing.T) {
	var capturedMain []byte
	srv := httptest.NewServer(dualHandler("DIRECT", vllmCompletionResponse(), &capturedMain))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"specific-model","messages":[{"role":"user","content":"hi"}],"temperature":0.8}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleChatCompletions(httptest.NewRecorder(), req)

	var out map[string]json.RawMessage
	json.Unmarshal(capturedMain, &out)

	var model string
	json.Unmarshal(out["model"], &model)
	if model != "specific-model" {
		t.Errorf("model: got %q, want specific-model", model)
	}

	var temp float64
	json.Unmarshal(out["temperature"], &temp)
	if temp != 0.8 {
		t.Errorf("temperature: got %v, want 0.8", temp)
	}
}

// --- handleGenericProxy ---

func TestHandleGenericProxy_ForwardsPath(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	s.handleGenericProxy(httptest.NewRecorder(), req)

	if capturedPath != "/v1/models" {
		t.Errorf("path: got %q, want /v1/models", capturedPath)
	}
}

func TestHandleGenericProxy_ForwardsMethod(t *testing.T) {
	var capturedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/tokenize", strings.NewReader(`{"text":"hello"}`))
	s.handleGenericProxy(httptest.NewRecorder(), req)

	if capturedMethod != "POST" {
		t.Errorf("method: got %q, want POST", capturedMethod)
	}
}

func TestHandleGenericProxy_ForwardsAuthHeader(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer token")
	s.handleGenericProxy(httptest.NewRecorder(), req)

	if capturedAuth != "Bearer token" {
		t.Errorf("Authorization: got %q, want Bearer token", capturedAuth)
	}
}

func TestHandleGenericProxy_ForwardsBody(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("POST", "/v1/tokenize", strings.NewReader(`{"text":"hello"}`))
	s.handleGenericProxy(httptest.NewRecorder(), req)

	if string(capturedBody) != `{"text":"hello"}` {
		t.Errorf("body: got %q, want %q", capturedBody, `{"text":"hello"}`)
	}
}

func TestHandleGenericProxy_CopiesResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer srv.Close()

	s := &server{cfg: testConfig(srv.URL)}
	req := httptest.NewRequest("GET", "/v1/models", nil)
	w := httptest.NewRecorder()
	s.handleGenericProxy(w, req)

	if w.Body.String() != `{"object":"list","data":[]}` {
		t.Errorf("response body: got %q", w.Body.String())
	}
}

// --- proxyStream ---

func TestProxyStream_CopiesAllBytes(t *testing.T) {
	data := "data: {\"content\":\"hello\"}\n\ndata: [DONE]\n\n"
	w := httptest.NewRecorder()
	proxyStream(w, strings.NewReader(data))

	if w.Body.String() != data {
		t.Errorf("body: got %q, want %q", w.Body.String(), data)
	}
}

func TestProxyStream_FlushesAfterEachChunk(t *testing.T) {
	w := httptest.NewRecorder()
	proxyStream(w, strings.NewReader("chunk"))

	// httptest.ResponseRecorder.Flushed is set to true when Flush() is called.
	if !w.Flushed {
		t.Error("expected Flush() to be called during streaming")
	}
}
