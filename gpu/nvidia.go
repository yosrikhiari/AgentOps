package gpu

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const smiQuery = "name,utilization.gpu,memory.used,memory.total,power.draw,temperature.gpu"

// NvidiaSMI reads the first GPU via the driver's own CLI.
type NvidiaSMI struct {
	Path    string        // "" means "nvidia-smi" from PATH
	Timeout time.Duration // default 3s
}

// NewNvidiaSMI returns a reader pointed at path (or PATH), with a 3s cap.
// The cap matters: a wedged driver must not wedge the prober goroutine.
func NewNvidiaSMI(path string) *NvidiaSMI {
	if path == "" {
		path = "nvidia-smi"
	}
	return &NvidiaSMI{Path: path, Timeout: 3 * time.Second}
}

func (n *NvidiaSMI) Read(ctx context.Context) (Sample, error) {
	if n.Timeout == 0 {
		n.Timeout = 3 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, n.Timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, n.Path,
		"--query-gpu="+smiQuery, "--format=csv,noheader,nounits").Output()
	if err != nil {
		return Sample{}, fmt.Errorf("nvidia-smi: %w", err)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	s, err := parseSMI(line)
	if err != nil {
		return Sample{}, err
	}
	s.At = time.Now().UTC()
	return s, nil
}

func parseSMI(line string) (Sample, error) {
	f := strings.Split(line, ",")
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	if len(f) != 6 {
		return Sample{}, fmt.Errorf("nvidia-smi: want 6 fields, got %d in %q", len(f), line)
	}
	util, err := strconv.ParseFloat(f[1], 64)
	if err != nil {
		return Sample{}, fmt.Errorf("nvidia-smi utilisation %q: %w", f[1], err)
	}
	usedMiB, err := strconv.ParseFloat(f[2], 64)
	if err != nil {
		return Sample{}, fmt.Errorf("nvidia-smi memory.used %q: %w", f[2], err)
	}
	totalMiB, err := strconv.ParseFloat(f[3], 64)
	if err != nil {
		return Sample{}, fmt.Errorf("nvidia-smi memory.total %q: %w", f[3], err)
	}
	return Sample{
		Name:          f[0],
		UtilPct:       util,
		MemUsedBytes:  int64(usedMiB * (1 << 20)),
		MemTotalBytes: int64(totalMiB * (1 << 20)),
		PowerW:        optionalFloat(f[4]),
		TempC:         optionalFloat(f[5]),
	}, nil
}

// optionalFloat returns nil for the driver's "[N/A]" and "[Not Supported]".
func optionalFloat(s string) *float64 {
	s = strings.Trim(s, "[] ")
	if s == "" || strings.EqualFold(s, "N/A") || strings.EqualFold(s, "Not Supported") {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}
