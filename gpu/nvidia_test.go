package gpu

import "testing"

func TestParseNvidiaSMI(t *testing.T) {
	line := "NVIDIA GeForce GTX 1650, 39, 77, 4096, 3.01, 64"
	s, err := parseSMI(line)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "NVIDIA GeForce GTX 1650" || s.UtilPct != 39 {
		t.Fatalf("got %+v", s)
	}
	if s.MemUsedBytes != 77*(1<<20) || s.MemTotalBytes != 4096*(1<<20) {
		t.Fatalf("memory not converted from MiB: %+v", s)
	}
	if s.PowerW == nil || *s.PowerW != 3.01 || s.TempC == nil || *s.TempC != 64 {
		t.Fatalf("power/temp: %+v", s)
	}
}

// TestParseSMIHandlesNotAvailable is the reason this is not a one-liner:
// laptop and datacenter drivers both report [N/A] for power, and older ones
// for temperature. A sentinel value would be a lie; nil is a missing fact.
func TestParseSMIHandlesNotAvailable(t *testing.T) {
	s, err := parseSMI("Tesla T4, 0, 0, 15360, [N/A], [N/A]")
	if err != nil {
		t.Fatal(err)
	}
	if s.PowerW != nil || s.TempC != nil {
		t.Fatalf("expected nil power/temp, got %+v", s)
	}
}

// TestParseSMIReadsTheThermalThrottleBit: the driver reports a bitmask of clock-event
// reasons. Bits 0x20 (SW thermal slowdown) and 0x40 (HW thermal slowdown) mean the card
// was hot enough to cut clocks - the difference between "slow because the model is big"
// and "slow because the GPU is cooking", which the eval comparability line needs.
func TestParseSMIReadsTheThermalThrottleBit(t *testing.T) {
	hot, err := parseSMI("NVIDIA GeForce GTX 1650, 88, 2048, 4096, 60.5, 84, 0x0000000000000021")
	if err != nil {
		t.Fatal(err)
	}
	if hot.Throttled == nil || !*hot.Throttled {
		t.Fatalf("SW thermal bit not read: %+v", hot)
	}
	cool, err := parseSMI("NVIDIA GeForce GTX 1650, 30, 1024, 4096, 40.0, 62, 0x0000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if cool.Throttled == nil || *cool.Throttled {
		t.Fatalf("idle-only mask must read as known-cool, not throttled: %+v", cool)
	}
	hw, err := parseSMI("NVIDIA GeForce GTX 1650, 90, 4096, 4096, 70.0, 91, 0x0000000000000041")
	if err != nil {
		t.Fatal(err)
	}
	if hw.Throttled == nil || !*hw.Throttled {
		t.Fatalf("HW thermal bit not read: %+v", hw)
	}
}

// TestParseSMILegacyQueryHasNoThrottleField: a driver that does not support
// clocks_event_reasons.active returns six fields. That is "unknown" (nil), not false:
// an old driver must not be reported as a cool card nobody measured.
func TestParseSMILegacyQueryHasNoThrottleField(t *testing.T) {
	s, err := parseSMI("Tesla T4, 0, 0, 15360, 10, 20")
	if err != nil {
		t.Fatal(err)
	}
	if s.Throttled != nil {
		t.Fatalf("legacy six-field row must read as unknown, got %v", *s.Throttled)
	}
	// A mask the driver answered but we cannot parse is also unknown, not cool.
	bad, err := parseSMI("Tesla T4, 0, 0, 15360, 10, 20, [Not Supported]")
	if err != nil {
		t.Fatal(err)
	}
	if bad.Throttled != nil {
		t.Fatalf("unparsable mask must read as unknown, got %v", *bad.Throttled)
	}
}

func TestParseSMIRejectsGarbage(t *testing.T) {
	if _, err := parseSMI("no commas here"); err == nil {
		t.Fatal("want error")
	}
	if _, err := parseSMI("Tesla T4, 0, 0, 15360, 10, 20, 30, 40"); err == nil {
		t.Fatal("want error on wrong field count")
	}
}
