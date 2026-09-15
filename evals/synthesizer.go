package evals

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Pair struct {
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	DocIDs   []string `json:"doc_ids"`
}

type Generator interface {
	Complete(prompt string) (string, error)
}

type OllamaGenerator struct {
	BaseURL string
	Model   string
	Client  *http.Client
	// Options is forwarded verbatim as Ollama "options" (e.g. {"temperature": 0}).
	Options map[string]any
}

func NewOllamaGenerator(baseURL, model string) *OllamaGenerator {
	return &OllamaGenerator{BaseURL: baseURL, Model: model, Client: &http.Client{Timeout: 300 * time.Second}}
}

func (g *OllamaGenerator) Complete(prompt string) (string, error) {
	req := map[string]any{"model": g.Model, "prompt": prompt, "stream": false}
	if len(g.Options) > 0 {
		req["options"] = g.Options
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	resp, err := g.Client.Post(g.BaseURL+"/api/generate", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &StatusError{Backend: "ollama", Code: resp.StatusCode}
	}
	var out struct {
		Response string `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.Response, nil
}

func draftPrompt(c Chunk) string {
	return fmt.Sprintf(`Write exactly 2 factual question-answer pairs answerable ONLY from the passage below.
Rules: answers use only passage facts, questions are specific, no preamble, output a raw JSON array, nothing else.
Each item: {"question": "...", "answer": "..."}.

Passage (doc %s):
%s`, c.DocID, c.Text)
}

func extractPairs(raw string, fallbackDoc string) []Pair {
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end <= start {
		return nil
	}
	var items []struct {
		Question string `json:"question"`
		Answer   string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &items); err != nil {
		return nil
	}
	var out []Pair
	for _, it := range items {
		q, a := strings.TrimSpace(it.Question), strings.TrimSpace(it.Answer)
		if q == "" || a == "" {
			continue
		}
		out = append(out, Pair{Question: q, Answer: a, DocIDs: []string{fallbackDoc}})
	}
	return out
}

func DraftPairs(gen Generator, chunks []Chunk) ([]Pair, error) {
	var out []Pair
	for _, c := range chunks {
		raw, err := gen.Complete(draftPrompt(c))
		if err != nil {
			return out, fmt.Errorf("draft %s: %w", c.DocID, err)
		}
		out = append(out, extractPairs(raw, c.DocID)...)
	}
	return out, nil
}
