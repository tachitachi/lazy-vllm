package proxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
)

// modelsResponse mirrors an OpenAI /v1/models response.
type modelsResponse struct {
	Object string            `json:"object"`
	Data   []json.RawMessage `json:"data"`
}

var passthroughHeaders = []string{
	"Authorization", "x-api-key", "Content-Type", "Accept",
	"anthropic-version", "anthropic-beta",
}

func (s *Server) HandleModels(w http.ResponseWriter, r *http.Request) {
	urls := uniqueURLs(s.Backends)
	if len(urls) == 0 {
		http.Error(w, "no backends configured", http.StatusBadGateway)
		return
	}

	type result struct {
		raw modelsResponse
		err error
	}
	ch := make(chan result, len(urls))
	var wg sync.WaitGroup

	for _, u := range urls {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u+"/v1/models", nil)
			if err != nil {
				ch <- result{err: fmt.Errorf("%s: %w", u, err)}
				return
			}
			for _, h := range passthroughHeaders {
				if v := r.Header.Get(h); v != "" {
					req.Header.Set(h, v)
				}
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				ch <- result{err: fmt.Errorf("%s: %w", u, err)}
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				ch <- result{err: fmt.Errorf("%s: %s", u, resp.Status)}
				return
			}
			var mr modelsResponse
			if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
				ch <- result{err: fmt.Errorf("%s: %w", u, err)}
				return
			}
			ch <- result{raw: mr}
		}(u)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	merged := make([]json.RawMessage, 0)
	seen := make(map[string]struct{})

	for res := range ch {
		if res.err != nil {
			slog.Warn("models query failed", "err", res.err)
			continue
		}
		for _, raw := range res.raw.Data {
			if raw == nil {
				continue
			}
			var top map[string]json.RawMessage
			if err := json.Unmarshal(raw, &top); err == nil {
				if id, ok := top["id"]; ok {
					if _, seen := seen[string(id)]; seen {
						continue
					}
					seen[string(id)] = struct{}{}
				}
			}
			merged = append(merged, raw)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(modelsResponse{Object: "list", Data: merged})
	slog.Info("models aggregated", "total", len(merged), "backends", len(urls))
}
