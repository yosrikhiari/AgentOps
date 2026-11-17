package gpu

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const smiQuery = "name,utilization.gpu,memory.used,memory.total,power.draw,temperature.gpu,clocks_event_reasons.active"

// smiQueryLegacy is the query for drivers without clocks_event_reasons.active.
const smiQueryLegacy = "name,utilization.gpu,memory.used,memory.total,power.draw,temperature.gpu"

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
		// Older drivers do not know clocks_event_reasons.active. Losing the throttle
		// signal is acceptable; losing the whole sample is not.
		out, err = exec.CommandContext(cctx, n.Path,
			"--query-gpu="+smiQueryLegacy, "--format=csv,noheader,nounits").Output()
		if err != nil {
			return Sample{}, fmt.Errorf("nvidia-smi: %w", err)
		}
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
	if len(f) != 6 && len(f) != 7 {
		return Sample{}, fmt.Errorf("nvidia-smi: want 6 or 7 fields, got %d in %q", len(f), line)
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
		Throttled:     len(f) == 7 && thermalBit(f[6]),
	}, nil
}

// thermalBit reads the clocks_event_reasons.active mask. Bits 0x20 (SW thermal slowdown)
// and 0x40 (HW thermal slowdown) are the two that mean "the card cut clocks because it is
// hot"; bit 0x1 (idle) is why a healthy mask is not zero. An unparsable mask is unknown,
// not throttled - the sample is still useful.
func thermalBit(mask string) bool {
	s := strings.TrimSpace(mask)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	v, err := strconv.ParseUint(s, 16, 64)
	if err != nil {
		return false
	}
	return v&0x20 != 0 || v&0x40 != 0
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
