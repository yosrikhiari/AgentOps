package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OllamaClient talks to a local Ollama server over its /api/chat endpoint.
type OllamaClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewOllamaClient(baseURL string) *OllamaClient {
	return &OllamaClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

func (c *OllamaClient) Name() string { return "ollama" }

// Local is true for the Ollama process itself; whether a *model* stays local is decided per
// ModelRef (Ollama ":cloud" models are hosted). See ModelRef.Local.
func (c *OllamaClient) Local() bool { return true }

type ollamaChatRequest struct {
	Model     string         `json:"model"`
	Messages  []Message      `json:"messages"`
	Stream    bool           `json:"stream"`
	Options   map[string]any `json:"options,omitempty"`
	Format    any            `json:"format,omitempty"`
	KeepAlive string         `json:"keep_alive,omitempty"`
}

type ollamaChatResponse struct {
	Message         Message `json:"message"`
	Done            bool    `json:"done"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	EvalCount       int     `json:"eval_count"`
	Error           string  `json:"error"`
}

func (c *OllamaClient) post(ctx context.Context, model string, msgs []Message, p GenParams, stream bool) (*http.Response, error) {
	req := ollamaChatRequest{Model: model, Messages: msgs, Stream: stream, Options: p.ollamaOptions(), Format: p.Format, KeepAlive: p.KeepAlive}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("ollama status %d", resp.StatusCode)
	}
	return resp, nil
}

func (c *OllamaClient) Generate(ctx context.Context, model string, msgs []Message, maxTokens int) (string, Usage, error) {
	return c.GenerateWith(ctx, model, msgs, GenParams{MaxTokens: maxTokens})
}

// GenerateWith is Generate with the full v1.1 parameter set: temperature, top_p, seed and
// stop land in Ollama `options` next to the passthrough map; `format` and `keep_alive` are
// top-level fields of /api/chat.
func (c *OllamaClient) GenerateWith(ctx context.Context, model string, msgs []Message, p GenParams) (string, Usage, error) {
	resp, err := c.post(ctx, model, msgs, p, false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	var out ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{}, err
	}
	if out.Error != "" {
		return "", Usage{}, fmt.Errorf("ollama: %s", out.Error)
	}
	u := Usage{PromptTokens: out.PromptEvalCount, CompletionTokens: out.EvalCount}
	u.TotalTokens = u.PromptTokens + u.CompletionTokens
	return out.Message.Content, u, nil
}

// Stream reads Ollama's newline-delimited JSON; the final object carries done:true and
// the token counts.
func (c *OllamaClient) Stream(ctx context.Context, model string, msgs []Message, maxTokens int, emit func(string)) (Usage, error) {
	return c.StreamWith(ctx, model, msgs, GenParams{MaxTokens: maxTokens}, emit)
}

// StreamWith is Stream with the full parameter set (see GenerateWith).
func (c *OllamaClient) StreamWith(ctx context.Context, model string, msgs []Message, p GenParams, emit func(string)) (Usage, error) {
	resp, err := c.post(ctx, model, msgs, p, true)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var u Usage
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var chunk ollamaChatResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			return u, err
		}
		if chunk.Error != "" {
			return u, fmt.Errorf("ollama: %s", chunk.Error)
		}
		if chunk.Message.Content != "" {
			emit(chunk.Message.Content)
		}
		if chunk.Done {
			u = Usage{PromptTokens: chunk.PromptEvalCount, CompletionTokens: chunk.EvalCount}
			u.TotalTokens = u.PromptTokens + u.CompletionTokens
			return u, nil
		}
	}
	if err := sc.Err(); err != nil {
		return u, err
	}
	return u, fmt.Errorf("ollama stream ended without done")
}

// Health is a cheap GET /api/tags — it answers even while a model is loading.
func (c *OllamaClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama status %d", resp.StatusCode)
	}
	return nil
}
