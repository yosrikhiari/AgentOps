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

func TestParseSMIRejectsGarbage(t *testing.T) {
	if _, err := parseSMI("no commas here"); err == nil {
		t.Fatal("want error")
	}
	if _, err := parseSMI("Tesla T4, 0, 0, 15360, 10, 20, 30, 40"); err == nil {
		t.Fatal("want error on wrong field count")
	}
}
