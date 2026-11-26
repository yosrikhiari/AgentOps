package router

import (
	"encoding/json"
	"testing"
)

func FuzzContentText(f *testing.F) {
	f.Add(`"hello"`)
	f.Add(`{"type":"text","text":"hi"}`)
	f.Add(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`)
	f.Add(`null`)
	f.Fuzz(func(t *testing.T, raw string) {
		// contentText must not panic on any input
		v := json.RawMessage(raw)
		_, _ = contentText(v)
	})
}
