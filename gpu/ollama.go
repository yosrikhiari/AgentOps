package gpu

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OllamaPS lists the models Ollama is holding in VRAM. This is the only
// per-model VRAM attribution that exists on this box: nvidia-smi reports one
// ollama process, so splitting its memory across models would be invention.
type OllamaPS struct {
	URL     string // e.g. the Ollama base URL; reuse OLLAMA_URL
	Timeout time.Duration
}

// Resident reads /api/ps once. An empty list is a normal answer (idle box),
// not an error; a non-200 status is an error that names the status.
func (o OllamaPS) Resident(ctx context.Context) ([]Resident, error) {
	if o.Timeout == 0 {
		o.Timeout = 3 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, o.URL+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama /api/ps: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/ps: status %d", resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name          string    `json:"name"`
			SizeVRAM      int64     `json:"size_vram"`
			ContextLength int       `json:"context_length"`
			ExpiresAt     time.Time `json:"expires_at"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("ollama /api/ps: %w", err)
	}
	out := make([]Resident, 0, len(body.Models))
	for _, m := range body.Models {
		out = append(out, Resident{Name: m.Name, SizeVRAMBytes: m.SizeVRAM,
			ContextLength: m.ContextLength, ExpiresAt: m.ExpiresAt})
	}
	return out, nil
}
