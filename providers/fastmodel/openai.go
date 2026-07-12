package fastmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// openAIBackend speaks the OpenAI-compatible chat completions shape — the
// Settings-configured endpoint optimization (spec §11). The credential is
// resolved from the named env var at call time; only the name is ever
// stored (§13).
type openAIBackend struct {
	baseURL   string
	apiKeyEnv string
	client    *http.Client
}

func (b *openAIBackend) Complete(ctx context.Context, system, prompt, model string) (string, provider.Usage, error) {
	var msgs []map[string]string
	if system != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": system})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": prompt})
	body, err := json.Marshal(map[string]any{"model": model, "messages": msgs})
	if err != nil {
		return "", provider.Usage{}, err
	}

	url := strings.TrimSuffix(b.baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", provider.Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if b.apiKeyEnv != "" {
		if key := os.Getenv(b.apiKeyEnv); key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}

	client := b.client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", provider.Usage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", provider.Usage{}, err
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", provider.Usage{}, &provider.RateLimitError{
			RetryAfter: retryAfter(resp.Header.Get("Retry-After")),
			Message:    fmt.Sprintf("endpoint %s returned 429", b.baseURL),
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", provider.Usage{}, fmt.Errorf("endpoint %s: status %d: %s",
			b.baseURL, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", provider.Usage{}, fmt.Errorf("endpoint response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", provider.Usage{}, fmt.Errorf("endpoint response has no choices")
	}
	usage := provider.Usage{
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
		Model:        parsed.Model,
	}
	return parsed.Choices[0].Message.Content, usage, nil
}

func retryAfter(h string) time.Duration {
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}
