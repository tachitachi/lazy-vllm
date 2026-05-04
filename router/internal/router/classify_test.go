package router

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassify_REASONING_ReturnsTrue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(vllmClassifyResponse("REASONING"))
	}))
	defer srv.Close()

	got, err := classify(context.Background(), nil, testConfig(srv.URL), "")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected true for REASONING response")
	}
}

func TestClassify_DIRECT_ReturnsFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(vllmClassifyResponse("DIRECT"))
	}))
	defer srv.Close()

	got, err := classify(context.Background(), nil, testConfig(srv.URL), "")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("expected false for DIRECT response")
	}
}

func TestClassify_TrimsWhitespaceAndNormalises(t *testing.T) {
	// Guided decoding should prevent this, but the parser must be robust.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(vllmClassifyResponse("  reasoning  "))
	}))
	defer srv.Close()

	got, err := classify(context.Background(), nil, testConfig(srv.URL), "")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected true: content '  reasoning  ' should normalise to REASONING")
	}
}

func TestClassify_FailOpen_HTTPError(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:1") // nothing listening
	got, err := classify(context.Background(), nil, cfg, "")
	if err == nil {
		t.Error("expected an error on connection failure")
	}
	if !got {
		t.Error("expected fail-open (true) when upstream is unreachable")
	}
}

func TestClassify_FailOpen_Non200Status(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	got, err := classify(context.Background(), nil, testConfig(srv.URL), "")
	if err == nil {
		t.Error("expected an error for non-200 status")
	}
	if !got {
		t.Error("expected fail-open (true) on non-200 response")
	}
}

func TestClassify_FailOpen_EmptyChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := json.Marshal(chatResponse{Choices: []chatChoice{}})
		w.Write(b)
	}))
	defer srv.Close()

	got, err := classify(context.Background(), nil, testConfig(srv.URL), "")
	if err == nil {
		t.Error("expected an error for empty choices array")
	}
	if !got {
		t.Error("expected fail-open (true) on empty choices")
	}
}

func TestClassify_FailOpen_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	got, err := classify(context.Background(), nil, testConfig(srv.URL), "")
	if err == nil {
		t.Error("expected an error for malformed JSON response")
	}
	if !got {
		t.Error("expected fail-open (true) on malformed JSON")
	}
}

func TestClassify_RequestStructure(t *testing.T) {
	var captured map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		w.Write(vllmClassifyResponse("DIRECT"))
	}))
	defer srv.Close()

	msgs := []ChatMessage{{Role: "user", Content: "hello"}}
	classify(context.Background(), msgs, testConfig(srv.URL), "")

	t.Run("model name", func(t *testing.T) {
		var model string
		json.Unmarshal(captured["model"], &model)
		if model != "test-model" {
			t.Errorf("got %q, want test-model", model)
		}
	})

	t.Run("stream is false", func(t *testing.T) {
		var stream bool
		json.Unmarshal(captured["stream"], &stream)
		if stream {
			t.Error("classify call must not use streaming")
		}
	})

	t.Run("structured_outputs.choice", func(t *testing.T) {
		var so map[string]json.RawMessage
		json.Unmarshal(captured["structured_outputs"], &so)
		var choices []string
		json.Unmarshal(so["choice"], &choices)
		if len(choices) != 2 || choices[0] != "DIRECT" || choices[1] != "REASONING" {
			t.Errorf("structured_outputs.choice: got %v, want [DIRECT REASONING]", choices)
		}
	})

	t.Run("chat_template_kwargs disables thinking", func(t *testing.T) {
		var kwargs map[string]bool
		json.Unmarshal(captured["chat_template_kwargs"], &kwargs)
		if kwargs["enable_thinking"] {
			t.Error("classify call must have enable_thinking=false")
		}
	})

	t.Run("routing system prompt is first message", func(t *testing.T) {
		var capturedMsgs []ChatMessage
		json.Unmarshal(captured["messages"], &capturedMsgs)
		if len(capturedMsgs) < 1 {
			t.Fatal("no messages in classify request")
		}
		if capturedMsgs[0].Role != "system" {
			t.Errorf("first message role: got %q, want system", capturedMsgs[0].Role)
		}
		if capturedMsgs[0].Content != routingPrompt {
			t.Errorf("first message content does not match routingPrompt")
		}
	})

	t.Run("user messages follow system prompt", func(t *testing.T) {
		var capturedMsgs []ChatMessage
		json.Unmarshal(captured["messages"], &capturedMsgs)
		// system + 1 user message
		if len(capturedMsgs) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(capturedMsgs))
		}
		if capturedMsgs[1].Role != "user" {
			t.Errorf("second message role: got %q, want user", capturedMsgs[1].Role)
		}
	})
}

func TestClassify_ForwardsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write(vllmClassifyResponse("DIRECT"))
	}))
	defer srv.Close()

	classify(context.Background(), nil, testConfig(srv.URL), "Bearer test-token")

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization: got %q, want %q", gotAuth, "Bearer test-token")
	}
}

func TestClassify_OmitsAuthHeader_WhenEmpty(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write(vllmClassifyResponse("DIRECT"))
	}))
	defer srv.Close()

	classify(context.Background(), nil, testConfig(srv.URL), "")

	if gotAuth != "" {
		t.Errorf("expected no Authorization header when empty, got %q", gotAuth)
	}
}
