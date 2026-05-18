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
		if strings.HasSuffix(id, "-FLASH") {
			expectedPrefix := strings.TrimSuffix(id, "-FLASH")
			if expectedPrefix != "model-a" && expectedPrefix != "model-b" {
				t.Errorf("unexpected flash model id: %q", id)
			}
		}
	}
}
