package proxy

import (
	"encoding/json"
	"strings"
	"testing"

	"lazy-vllm-proxy/internal/config"
)

func TestResolveBackend(t *testing.T) {
	backends := []config.Backend{
		{Name: "gemma", URL: "http://localhost:8000"},
		{Name: "qwen", URL: "http://localhost:8001"},
		{Name: "llama", URL: "http://localhost:8002"},
	}

	tests := []struct {
		model string
		want  string
	}{
		{"gemma", "http://localhost:8000"},
		{"qwen", "http://localhost:8001"},
		{"llama", "http://localhost:8002"},
		{"gemma-3-4b", "http://localhost:8000"},
		{"qwen-3-8b", "http://localhost:8000"},
		{"llama-3-70b", "http://localhost:8000"},
		{"unknown-model", "http://localhost:8000"},
		{"", "http://localhost:8000"},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := resolveBackend(backends, tt.model)
			if got != tt.want {
				t.Errorf("resolveBackend(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestResolveBackend_Exact(t *testing.T) {
	backends := []config.Backend{
		{Name: "gemma", URL: "http://localhost:8000"},
	}
	got := resolveBackend(backends, "gemma-2-4b")
	if got != "http://localhost:8000" {
		t.Errorf("resolveBackend(%q, %q) = %q, expected first backend fallback", "gemma-2-4b", "gemma", got)
	}
}

func TestResolveBackend_Empty(t *testing.T) {
	got := resolveBackend(nil, "anything")
	if got != "" {
		t.Errorf("resolveBackend(nil, _) = %q, want empty", got)
	}
}

func TestUniqueURLs(t *testing.T) {
	tests := []struct {
		name  string
		input []config.Backend
		want  []string
	}{
		{
			name: "all unique",
			input: []config.Backend{
				{Name: "a", URL: "http://srv1"},
				{Name: "b", URL: "http://srv2"},
				{Name: "c", URL: "http://srv3"},
			},
			want: []string{"http://srv1", "http://srv2", "http://srv3"},
		},
		{
			name: "duplicates",
			input: []config.Backend{
				{Name: "a", URL: "http://srv1"},
				{Name: "b", URL: "http://srv2"},
				{Name: "c", URL: "http://srv1"},
			},
			want: []string{"http://srv1", "http://srv2"},
		},
		{
			name:  "empty",
			input: nil,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uniqueURLs(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("uniqueURLs() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("uniqueURLs()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractModel(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		want      string
		wantModel string // expected model field in the cleaned body
	}{
		{
			name:      "openai format",
			body:      []byte(`{"model":"gemma-3-4b","messages":[],"stream":false}`),
			want:      "gemma-3-4b",
			wantModel: "gemma-3-4b",
		},
		{
			name:      "openai stream",
			body:      []byte(`{"model":"qwen-3-8b","messages":[],"stream":true}`),
			want:      "qwen-3-8b",
			wantModel: "qwen-3-8b",
		},
		{
			name:      "flash model strips suffix in body",
			body:      []byte(`{"model":"qwen3.6-FLASH","messages":[],"stream":false}`),
			want:      "qwen3.6-FLASH",
			wantModel: "qwen3.6",
		},
		{
			name: "missing model",
			body: []byte(`{"messages":[]}`),
			want: "",
		},
		{
			name: "invalid json",
			body: []byte(`not json`),
			want: "",
		},
		{
			name: "empty body",
			body: []byte(``),
			want: "",
		},
		{
			name: "model is empty string",
			body: []byte(`{"model":""}`),
			want: "",
		},
		{
			name: "number only",
			body: []byte(`42`),
			want: "",
		},
		{
			name: "array",
			body: []byte(`["model"]`),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cleanedBody := extractModel(tt.body)
			if got != tt.want {
				t.Errorf("extractModel(%q) = %q, want %q", tt.body, got, tt.want)
			}
			if tt.wantModel != "" {
				var obj map[string]any
				if err := json.Unmarshal(cleanedBody, &obj); err != nil {
					t.Errorf("failed to unmarshal cleaned body: %v", err)
				} else if model, ok := obj["model"].(string); !ok || model != tt.wantModel {
					t.Errorf("cleaned body model = %q, want %q", model, tt.wantModel)
				}
			}
		})
	}
}

func TestStripFlash(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"qwen3.6-FLASH", "qwen3.6"},
		{"model-FLASH", "model"},
		{"gemma-3-4b", "gemma-3-4b"},
		{"", ""},
		{"FLASH", "FLASH"},
		{"my-model-FLASH-FLASH", "my-model-FLASH"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := stripFlash(tt.in)
			if got != tt.want {
				t.Errorf("stripFlash(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsFlashModel(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"qwen3.6-FLASH", true},
		{"gemma-3-4b", false},
		{"", false},
		{"FLASH", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := isFlashModel(tt.in)
			if got != tt.want {
				t.Errorf("isFlashModel(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestInjectDisableThinking(t *testing.T) {
	t.Run("adds chat_template_kwargs", func(t *testing.T) {
		body := []byte(`{"model":"test","messages":[]}`)
		result := injectDisableThinking(body)
		var obj map[string]any
		if err := json.Unmarshal(result, &obj); err != nil {
			t.Fatal(err)
		}
		kw, ok := obj["chat_template_kwargs"].(map[string]any)
		if !ok {
			t.Fatal("missing chat_template_kwargs")
		}
		val, _ := kw["enable_thinking"].(bool)
		if val {
			t.Error("enable_thinking should be false")
		}
	})

	t.Run("preserves existing fields", func(t *testing.T) {
		body := []byte(`{"model":"test","messages":[],"stream":true}`)
		result := injectDisableThinking(body)
		var obj map[string]any
		if err := json.Unmarshal(result, &obj); err != nil {
			t.Fatal(err)
		}
		if _, ok := obj["model"]; !ok {
			t.Error("model field should be preserved")
		}
		if _, ok := obj["messages"]; !ok {
			t.Error("messages field should be preserved")
		}
		if stream, ok := obj["stream"].(bool); !ok || !stream {
			t.Error("stream field should be preserved")
		}
	})

	t.Run("invalid json returns original", func(t *testing.T) {
		body := []byte(`not json`)
		result := injectDisableThinking(body)
		if string(result) != "not json" {
			t.Error("invalid json should return original body")
		}
	})

	t.Run("strips flash model name", func(t *testing.T) {
		body := []byte(`{"model":"qwen3.6-FLASH","messages":[],"stream":false}`)
		result := injectDisableThinking(body)
		var obj map[string]any
		if err := json.Unmarshal(result, &obj); err != nil {
			t.Fatal(err)
		}
		model, ok := obj["model"].(string)
		if !ok || model != "qwen3.6" {
			t.Errorf("model = %q, want qwen3.6", model)
		}
	})
}

func TestFlashBackendRouting(t *testing.T) {
	backends := []config.Backend{
		{Name: "qwen3.6", URL: "http://localhost:8001"},
	}
	// Flash model resolves to the non-flash backend
	got := resolveBackend(backends, stripFlash("qwen3.6-FLASH"))
	if got != "http://localhost:8001" {
		t.Errorf("expected %q, got %q", "http://localhost:8001", got)
	}
}

func TestCloneModelWithFlash(t *testing.T) {
	t.Run("clones model with FLASH suffix", func(t *testing.T) {
		raw := json.RawMessage(`{"id":"RedHatAI/Qwen3.6-35B","object":"model","root":"RedHatAI/Qwen3.6-35B"}`)
		flash := cloneModelWithFlash(raw)
		if flash == nil {
			t.Fatal("cloneModelWithFlash returned nil")
		}
		var obj map[string]any
		if err := json.Unmarshal(flash, &obj); err != nil {
			t.Fatal(err)
		}
		id, _ := obj["id"].(string)
		if !strings.HasSuffix(id, "-FLASH") {
			t.Errorf("id %q should end with -FLASH", id)
		}
		root, _ := obj["root"].(string)
		if !strings.HasSuffix(root, "-FLASH") {
			t.Errorf("root %q should end with -FLASH", root)
		}
	})

	t.Run("skips if no id field", func(t *testing.T) {
		raw := json.RawMessage(`{"object":"model"}`)
		flash := cloneModelWithFlash(raw)
		if flash != nil {
			t.Error("expected nil for model without id")
		}
	})

	t.Run("root not updated when different from id", func(t *testing.T) {
		raw := json.RawMessage(`{"id":"model-a","root":"model-b","object":"model"}`)
		flash := cloneModelWithFlash(raw)
		if flash == nil {
			t.Fatal("cloneModelWithFlash returned nil")
		}
		var obj map[string]any
		if err := json.Unmarshal(flash, &obj); err != nil {
			t.Fatal(err)
		}
		id, _ := obj["id"].(string)
		if id != "model-a-FLASH" {
			t.Errorf("id = %q, want model-a-FLASH", id)
		}
		root, _ := obj["root"].(string)
		if root != "model-b" {
			t.Errorf("root should be unchanged: got %q", root)
		}
	})
}

func TestInjectFlashModels(t *testing.T) {
	raw1 := json.RawMessage(`{"id":"model-a","object":"model"}`)
	raw2 := json.RawMessage(`{"id":"model-b","object":"model"}`)
	models := []json.RawMessage{raw1, raw2}
	result := injectFlashModels(models)
	// Should have 4 models: 2 originals + 2 flash clones
	if len(result) != 4 {
		t.Fatalf("expected 4 models, got %d", len(result))
	}
	// Check that flash clones have the right ids
	for _, raw := range result {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		id, _ := obj["id"].(string)
		if before, ok := strings.CutSuffix(id, "-FLASH"); ok {
			expectedPrefix := before
			if expectedPrefix != "model-a" && expectedPrefix != "model-b" {
				t.Errorf("unexpected flash model id: %q", id)
			}
		}
	}
}

// --- effortTokenBudget ---

func TestEffortTokenBudget(t *testing.T) {
	tests := []struct {
		effort      string
		wantTokens  int
		wantDisable bool
	}{
		{"none", 0, true},
		{"NONE", 0, true},
		{"minimal", 128, false},
		{"low", 256, false},
		{"medium", 1024, false},
		{"high", 4096, false},
		{"xhigh", 8192, false},
		{"max", 32000, false},
		{"MAX", 32000, false},
		{"", 32000, false},
		{"unknown", 32000, false},
	}
	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			tokens, disable := effortTokenBudget(tt.effort)
			if tokens != tt.wantTokens || disable != tt.wantDisable {
				t.Errorf("effortTokenBudget(%q) = (%d, %v), want (%d, %v)",
					tt.effort, tokens, disable, tt.wantTokens, tt.wantDisable)
			}
		})
	}
}

// --- extractEffort ---

func TestExtractEffort_OpenAI(t *testing.T) {
	body := []byte(`{"model":"test","reasoning_effort":"high","messages":[]}`)
	got := extractEffort(body)
	if got != "high" {
		t.Errorf("extractEffort = %q, want high", got)
	}
}

func TestExtractEffort_Anthropic(t *testing.T) {
	body := []byte(`{"model":"test","output_config":{"effort":"medium"},"messages":[]}`)
	got := extractEffort(body)
	if got != "medium" {
		t.Errorf("extractEffort = %q, want medium", got)
	}
}

func TestExtractEffort_OpenAITakesPrecedence(t *testing.T) {
	body := []byte(`{"reasoning_effort":"high","output_config":{"effort":"low"}}`)
	got := extractEffort(body)
	if got != "high" {
		t.Errorf("extractEffort = %q, want high (openai takes precedence)", got)
	}
}

func TestExtractEffort_Missing(t *testing.T) {
	body := []byte(`{"model":"test","messages":[]}`)
	got := extractEffort(body)
	if got != "" {
		t.Errorf("extractEffort = %q, want empty", got)
	}
}

func TestExtractEffort_InvalidJSON(t *testing.T) {
	got := extractEffort([]byte("not json"))
	if got != "" {
		t.Errorf("extractEffort(invalid) = %q, want empty", got)
	}
}

// --- extractMaxTokens ---

func TestExtractMaxTokens_Present(t *testing.T) {
	body := []byte(`{"max_tokens":16000,"messages":[]}`)
	got := extractMaxTokens(body, 32000)
	if got != 16000 {
		t.Errorf("extractMaxTokens = %d, want 16000", got)
	}
}

func TestExtractMaxTokens_Missing(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	got := extractMaxTokens(body, 32000)
	if got != 32000 {
		t.Errorf("extractMaxTokens(missing) = %d, want 32000", got)
	}
}

func TestExtractMaxTokens_Zero(t *testing.T) {
	body := []byte(`{"max_tokens":0}`)
	got := extractMaxTokens(body, 32000)
	if got != 32000 {
		t.Errorf("extractMaxTokens(0) = %d, want 32000 (default)", got)
	}
}

// --- computeThinkingBudget ---

func TestComputeThinkingBudget_EffortNone_DisablesThinking(t *testing.T) {
	body := []byte(`{"max_tokens":32000,"reasoning_effort":"none"}`)
	budget, disable := computeThinkingBudget(body, 0.9)
	if !disable {
		t.Error("expected disable=true for effort=none")
	}
	if budget != 0 {
		t.Errorf("budget = %d, want 0", budget)
	}
}

func TestComputeThinkingBudget_EffortHigh_CapByFraction(t *testing.T) {
	// max_tokens=32000, fraction=0.5 → cap=16000; effort=high → 4096
	// min(4096, 16000) = 4096
	body := []byte(`{"max_tokens":32000,"reasoning_effort":"high"}`)
	budget, disable := computeThinkingBudget(body, 0.5)
	if disable {
		t.Error("expected disable=false")
	}
	if budget != 4096 {
		t.Errorf("budget = %d, want 4096", budget)
	}
}

func TestComputeThinkingBudget_FractionCapSmaller(t *testing.T) {
	// max_tokens=1000, fraction=0.9 → cap=900; effort missing → 32000
	// min(32000, 900) = 900
	body := []byte(`{"max_tokens":1000}`)
	budget, disable := computeThinkingBudget(body, 0.9)
	if disable {
		t.Error("expected disable=false")
	}
	if budget != 900 {
		t.Errorf("budget = %d, want 900", budget)
	}
}

func TestComputeThinkingBudget_NoMaxTokens_UsesDefault(t *testing.T) {
	// no max_tokens → default 32000; fraction=0.9 → cap=28800; effort=high → 4096
	body := []byte(`{"reasoning_effort":"high"}`)
	budget, disable := computeThinkingBudget(body, 0.9)
	if disable {
		t.Error("expected disable=false")
	}
	if budget != 4096 {
		t.Errorf("budget = %d, want 4096", budget)
	}
}

func TestComputeThinkingBudget_AnthropicEffort(t *testing.T) {
	body := []byte(`{"max_tokens":32000,"output_config":{"effort":"xhigh"}}`)
	budget, disable := computeThinkingBudget(body, 0.9)
	if disable {
		t.Error("expected disable=false")
	}
	if budget != 8192 {
		t.Errorf("budget = %d, want 8192", budget)
	}
}

// --- injectThinkingTokenBudget ---

func TestInjectThinkingTokenBudget_AddsField(t *testing.T) {
	body := []byte(`{"model":"test","messages":[]}`)
	result, err := injectThinkingTokenBudget(body, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatal(err)
	}
	budget, ok := obj["thinking_token_budget"].(float64)
	if !ok {
		t.Fatal("thinking_token_budget missing or wrong type")
	}
	if int(budget) != 4096 {
		t.Errorf("thinking_token_budget = %d, want 4096", int(budget))
	}
}

func TestInjectThinkingTokenBudget_PreservesFields(t *testing.T) {
	body := []byte(`{"model":"test","max_tokens":8192,"messages":[]}`)
	result, err := injectThinkingTokenBudget(body, 1024)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]any
	if err := json.Unmarshal(result, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["model"]; !ok {
		t.Error("model field should be preserved")
	}
	if maxTok, ok := obj["max_tokens"].(float64); !ok || int(maxTok) != 8192 {
		t.Error("max_tokens field should be preserved")
	}
}

func TestInjectThinkingTokenBudget_InvalidJSON_ReturnsError(t *testing.T) {
	_, err := injectThinkingTokenBudget([]byte("not json"), 1024)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
