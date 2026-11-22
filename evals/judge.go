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

const JudgePromptVersion = "faithfulness-v2"

// Label is the judge's verdict on one claim. Three labels, not two: "the context says
// the opposite" and "the context does not mention it" are different failures with
// different fixes (wrong retrieval vs missing knowledge), and collapsing them was the
// reason a 0.00 score could not be explained.
type Label int

const (
	Entailed Label = iota
	Contradicted
	Baseless
)

func (l Label) String() string {
	switch l {
	case Entailed:
		return "entailed"
	case Contradicted:
		return "contradicted"
	default:
		return "baseless"
	}
}

// Supported is the boundary the scorer and the existing binary metrics use: only an
// entailed claim counts toward faithfulness. Both negative labels fail closed.
func (l Label) Supported() bool { return l == Entailed }

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
	JudgeClaim(ctx context.Context, claim, contextText string) (Label, string, error)
}

func faithfulnessPrompt(claim, contextText string) string {
	return fmt.Sprintf(`Decide how the CLAIM relates to the CONTEXT. Judge only the context, not what you know.
First write 1-2 sentences of evidence-based justification quoting the context.
Then on the last line write exactly one of:
VERDICT: ENTAILED (the context states it or directly implies it)
VERDICT: CONTRADICTED (the context states the opposite)
VERDICT: BASELESS (the context neither supports nor contradicts it)

CONTEXT:
%s

CLAIM:
%s`, contextText, claim)
}

// lastVerdictIndex finds the byte offset of the last "VERDICT:" marker in s,
// matched case-insensitively on ASCII bytes. Working on bytes (not on a
// case-folded copy) keeps the returned offset valid for slicing s even when s
// contains non-ASCII or invalid UTF-8 that a case fold would re-encode.
func lastVerdictIndex(s string) int {
	const marker = "VERDICT:"
	b := []byte(s)
	mlen := len(marker)
	if len(b) < mlen {
		return -1
	}
	for i := len(b) - mlen; i >= 0; i-- {
		ok := true
		for j := 0; j < mlen; j++ {
			c := b[i+j]
			m := marker[j]
			// case-fold ASCII only
			if c >= 'a' && c <= 'z' {
				c -= 32
			}
			if m >= 'a' && m <= 'z' {
				m -= 32
			}
			if c != m {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

// parseVerdict reads the label after the last VERDICT: marker. Only the first token after
// the marker counts: a label followed by an explanation ("BASELESS - the context neither
// supports nor contradicts it") must not be read as the words inside the explanation.
// A missing or unrecognised verdict is Baseless - fail closed, as before.
func parseVerdict(raw string) (Label, string) {
	idx := lastVerdictIndex(raw)
	just := strings.TrimSpace(raw)
	if idx < 0 {
		return Baseless, just
	}
	just = strings.TrimSpace(raw[:idx])
	rest := strings.TrimSpace(raw[idx+len("VERDICT:"):])
	// case-fold the verdict token for matching, without re-slicing
	upperRest := strings.ToUpper(rest)
	if i := strings.IndexAny(rest, "\r\n"); i >= 0 {
		rest = rest[:i]
		upperRest = strings.ToUpper(rest)
	}
	rest = strings.TrimLeft(strings.TrimSpace(upperRest), "*-: ")
	switch {
	case strings.HasPrefix(rest, "ENTAILED"):
		return Entailed, just
	case strings.HasPrefix(rest, "CONTRADICT"):
		return Contradicted, just
	case strings.HasPrefix(rest, "BASELESS"), strings.HasPrefix(rest, "NOT IN CONTEXT"):
		return Baseless, just
	case strings.HasPrefix(rest, "NOT SUPPORTED"), strings.HasPrefix(rest, "UNSUPPORTED"), strings.HasPrefix(rest, "REFUTED"):
		// v1 asked for SUPPORTED/REFUTED, a vocabulary that cannot tell a contradiction
		// from an absence. Read it as baseless: unsupported without inventing a stronger
		// claim than the judge made.
		return Baseless, just
	case strings.HasPrefix(rest, "SUPPORTED"):
		return Entailed, just
	}
	return Baseless, just
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

func (j *OllamaJudge) JudgeClaim(ctx context.Context, claim, contextText string) (Label, string, error) {
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
		return Baseless, "", err
	}
	label, just := parseVerdict(raw)
	return label, just, nil
}

type GroqJudge struct {
	Model  string
	APIKey string
	Client *http.Client
}

func NewGroqJudge(model, apiKey string) *GroqJudge {
	return &GroqJudge{Model: model, APIKey: apiKey, Client: &http.Client{Timeout: 120 * time.Second}}
}

func (j *GroqJudge) JudgeClaim(ctx context.Context, claim, contextText string) (Label, string, error) {
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
		return Baseless, "", err
	}
	label, just := parseVerdict(raw)
	return label, just, nil
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
