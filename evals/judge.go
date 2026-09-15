package evals

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const JudgePromptVersion = "faithfulness-v1"

type RateLimitError struct {
	After time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited, retry after %s", e.After)
}

// StatusError is a non-2xx reply from a judge backend. 4xx (other than 429) is the
// caller's fault — bad key, bad model name, oversized prompt — and is never retried.
type StatusError struct {
	Backend string
	Code    int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s status %d", e.Backend, e.Code)
}

// Permanent reports whether err will not get better by trying again.
func Permanent(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code >= 400 && se.Code < 500 && se.Code != 429
	}
	return false
}

func WithRetry(ctx context.Context, attempts int, base time.Duration, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if Permanent(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := base * time.Duration(1<<uint(i))
		var rl *RateLimitError
		if errors.As(err, &rl) {
			wait *= 2
			if rl.After > wait {
				wait = rl.After
			}
		}
		wait += time.Duration(rand.Int63n(int64(base)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return err
}

type Judge interface {
	JudgeClaim(ctx context.Context, claim, contextText string) (supported bool, justification string, err error)
}

func faithfulnessPrompt(claim, contextText string) string {
	return fmt.Sprintf(`Decide if the CLAIM is fully supported by the CONTEXT. Ignore answer length and style.
First write 1-2 sentences of evidence-based justification quoting the context.
Then on the last line write exactly: VERDICT: SUPPORTED or VERDICT: REFUTED.

CONTEXT:
%s

CLAIM:
%s`, contextText, claim)
}

func parseVerdict(raw string) (bool, string) {
	upper := strings.ToUpper(raw)
	idx := strings.LastIndex(upper, "VERDICT:")
	just := strings.TrimSpace(raw)
	if idx >= 0 {
		just = strings.TrimSpace(raw[:idx])
		rest := upper[idx:]
		// Judges paraphrase: "REFUTED", "NOT SUPPORTED", "UNSUPPORTED" all mean no.
		if strings.Contains(rest, "REFUTED") || strings.Contains(rest, "NOT SUPPORTED") || strings.Contains(rest, "UNSUPPORTED") {
			return false, just
		}
		if strings.Contains(rest, "SUPPORTED") {
			return true, just
		}
		return false, just
	}
	return false, just
}

type OllamaJudge struct {
	Model string
	Gen   *OllamaGenerator
}

// NewOllamaJudge pins temperature to 0 so the same claim gets the same verdict on rerun —
// the Groq judge already does; a sampled verdict turned one golden pair into a coin flip.
func NewOllamaJudge(baseURL, model string) *OllamaJudge {
	gen := NewOllamaGenerator(baseURL, model)
	gen.Options = map[string]any{"temperature": 0}
	return &OllamaJudge{Model: model, Gen: gen}
}

func (j *OllamaJudge) JudgeClaim(ctx context.Context, claim, contextText string) (bool, string, error) {
	var raw string
	err := WithRetry(ctx, 3, time.Second, func() error {
		r, err := j.Gen.Complete(faithfulnessPrompt(claim, contextText))
		if err != nil {
			return err
		}
		raw = r
		return nil
	})
	if err != nil {
		return false, "", err
	}
	ok, just := parseVerdict(raw)
	return ok, just, nil
}

type GroqJudge struct {
	Model  string
	APIKey string
	Client *http.Client
}

func NewGroqJudge(model, apiKey string) *GroqJudge {
	return &GroqJudge{Model: model, APIKey: apiKey, Client: &http.Client{Timeout: 120 * time.Second}}
}

func (j *GroqJudge) JudgeClaim(ctx context.Context, claim, contextText string) (bool, string, error) {
	var raw string
	err := WithRetry(ctx, 4, 2*time.Second, func() error {
		r, err := j.complete(ctx, claim, contextText)
		if err != nil {
			return err
		}
		raw = r
		return nil
	})
	if err != nil {
		return false, "", err
	}
	ok, just := parseVerdict(raw)
	return ok, just, nil
}

func (j *GroqJudge) complete(ctx context.Context, claim, contextText string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model": j.Model,
		"messages": []any{
			map[string]any{"role": "user", "content": faithfulnessPrompt(claim, contextText)},
		},
		"temperature": 0,
		"max_tokens":  512,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.groq.com/openai/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+j.APIKey)
	resp, err := j.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return "", &RateLimitError{After: time.Duration(after) * time.Second}
	}
	if resp.StatusCode != http.StatusOK {
		return "", &StatusError{Backend: "groq", Code: resp.StatusCode}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("empty groq response")
	}
	return out.Choices[0].Message.Content, nil
}
