package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OpenAIBackend talks to any OpenAI-compatible chat API (Groq, OpenAI, vLLM, LM Studio…).
// It is a cloud backend unless IsLocal is set (e.g. a vLLM box on the LAN).
type OpenAIBackend struct {
	BackendName string
	BaseURL     string // e.g. https://api.groq.com/openai/v1
	APIKey      string
	IsLocal     bool
	HTTPClient  *http.Client
}

func NewOpenAIBackend(name, baseURL, apiKey string) *OpenAIBackend {
	return &OpenAIBackend{
		BackendName: name,
		BaseURL:     strings.TrimRight(baseURL, "/"),
		APIKey:      apiKey,
		HTTPClient:  &http.Client{Timeout: 300 * time.Second},
	}
}

func (b *OpenAIBackend) Name() string { return b.BackendName }
func (b *OpenAIBackend) Local() bool  { return b.IsLocal }

type oaiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func (b *OpenAIBackend) post(ctx context.Context, model string, msgs []Message, p GenParams, stream bool) (*http.Response, error) {
	req := map[string]any{"model": model, "messages": msgs, "stream": stream}
	p.openAIBody(req)
	if stream {
		req["stream_options"] = map[string]any{"include_usage": true}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if b.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+b.APIKey)
	}
	resp, err := b.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s status %d", b.BackendName, resp.StatusCode)
	}
	return resp, nil
}

func (b *OpenAIBackend) Generate(ctx context.Context, model string, msgs []Message, maxTokens int) (string, Usage, error) {
	return b.GenerateWith(ctx, model, msgs, GenParams{MaxTokens: maxTokens})
}

// GenerateWith forwards temperature, top_p, seed, stop and response_format. Ollama-only
// knobs (options, keep_alive) are dropped — see GenParams.openAIBody.
func (b *OpenAIBackend) GenerateWith(ctx context.Context, model string, msgs []Message, p GenParams) (string, Usage, error) {
	resp, err := b.post(ctx, model, msgs, p, false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
		Usage oaiUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", Usage{}, err
	}
	if len(out.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("%s: empty choices", b.BackendName)
	}
	return out.Choices[0].Message.Content, Usage(out.Usage), nil
}

// Stream parses server-sent events: "data: {...}" lines until "data: [DONE]".
func (b *OpenAIBackend) Stream(ctx context.Context, model string, msgs []Message, maxTokens int, emit func(string)) (Usage, error) {
	return b.StreamWith(ctx, model, msgs, GenParams{MaxTokens: maxTokens}, emit)
}

// StreamWith is Stream with the full parameter set (see GenerateWith).
func (b *OpenAIBackend) StreamWith(ctx context.Context, model string, msgs []Message, p GenParams, emit func(string)) (Usage, error) {
	resp, err := b.post(ctx, model, msgs, p, true)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var u Usage
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return u, nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *oaiUsage `json:"usage"`
			XGroq *struct {
				Usage *oaiUsage `json:"usage"`
			} `json:"x_groq"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return u, err
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			emit(chunk.Choices[0].Delta.Content)
		}
		if chunk.Usage != nil {
			u = Usage(*chunk.Usage)
		} else if chunk.XGroq != nil && chunk.XGroq.Usage != nil {
			u = Usage(*chunk.XGroq.Usage)
		}
	}
	if err := sc.Err(); err != nil {
		return u, err
	}
	return u, nil
}

// Health lists models — the cheapest authenticated call every OpenAI-compatible API has.
func (b *OpenAIBackend) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.BaseURL+"/models", nil)
	if err != nil {
		return err
	}
	if b.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.APIKey)
	}
	resp, err := b.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s status %d", b.BackendName, resp.StatusCode)
	}
	return nil
}
