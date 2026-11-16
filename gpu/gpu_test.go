package gpu

import "testing"

// TestSampleHasOneUnit proves the whole package agrees that memory is bytes.
// nvidia-smi speaks MiB; the conversion happens once, at the reader boundary,
// so no caller can be off by 2^20.
func TestSampleHasOneUnit(t *testing.T) {
	s := Sample{MemUsedBytes: 1024 * 1024, MemTotalBytes: 4 * 1024 * 1024}
	if got := s.UsedPercent(); got != 25 {
		t.Fatalf("UsedPercent = %v, want 25", got)
	}
	if s.UsedGB() != 0.0009765625 {
		t.Fatalf("UsedGB = %v", s.UsedGB())
	}
}
