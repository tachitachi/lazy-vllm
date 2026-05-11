package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"agent-graph/internal/agent"
	"agent-graph/internal/graphs"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		fmt.Fprintf(os.Stderr, "error: %s is required\n", key)
		os.Exit(1)
	}
	return v
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "DEBUG":
		return slog.LevelDebug
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func availableAgents() string {
	names := graphs.Names()
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func extractText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return fmt.Sprintf("%v", v)
	}
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s <agent-name> <prompt>\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Available agents: %s\n\n", availableAgents())
		fmt.Fprintf(os.Stderr, "Environment variables:\n")
		fmt.Fprintf(os.Stderr, "  PROXY_URL            proxy address (required, e.g. http://localhost:8002)\n")
		fmt.Fprintf(os.Stderr, "  MODEL_NAME           model to use (required)\n")
		fmt.Fprintf(os.Stderr, "  ROUTER_WINDOW_SIZE   message history window (default 0 = unlimited)\n")
		fmt.Fprintf(os.Stderr, "  API_KEY              forwarded as Authorization: Bearer <key>\n")
		fmt.Fprintf(os.Stderr, "  LOG_LEVEL            DEBUG|INFO|WARN|ERROR (default INFO)\n")
	}
	flag.Parse()

	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(1)
	}
	agentName := flag.Arg(0)
	prompt := flag.Arg(1)

	levelVar := &slog.LevelVar{}
	levelVar.Set(parseLogLevel(envOr("LOG_LEVEL", "INFO")))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: levelVar})))

	factory := graphs.Lookup(agentName)
	if factory == nil {
		fmt.Fprintf(os.Stderr, "unknown agent %q\navailable: %s\n", agentName, availableAgents())
		os.Exit(1)
	}

	proxyURL := mustEnv("PROXY_URL")
	modelName := mustEnv("MODEL_NAME")
	cfg := agent.Config{
		ModelName:  modelName,
		WindowSize: envIntOr("ROUTER_WINDOW_SIZE", 0),
	}

	g := factory(cfg)

	headers := make(http.Header)
	if key := os.Getenv("API_KEY"); key != "" {
		headers.Set("Authorization", "Bearer "+key)
	}

	pctx := &agent.PipelineCtx{
		OriginalBody:    []byte(`{}`),
		OriginalHeaders: headers,
		Stream:          false,
		Format:          agent.FormatOpenAI,
		Messages:        []agent.ChatMessage{{Role: "user", Content: prompt}},
		NodeOutputs:     make(map[string]string),
		BackendURL:      proxyURL,
	}

	if err := g.Run(context.Background(), pctx, nil); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	for i := len(pctx.Messages) - 1; i >= 0; i-- {
		if pctx.Messages[i].Role == "assistant" {
			fmt.Println(extractText(pctx.Messages[i].Content))
			return
		}
	}

	fmt.Fprintln(os.Stderr, "error: no assistant response in output")
	os.Exit(1)
}
