package scan

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func exampleScan(tb testing.TB) []byte {
	tb.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "examples", "scan.json"))
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func TestExampleScanRoundTripsByteForByte(t *testing.T) {
	ex := exampleScan(t)
	got, err := Normalize(ex)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, ex) {
		t.Errorf("examples/scan.json is not in the canonical form Cubit prints:\n%s", got)
	}
}

func TestDecodeIsStrict(t *testing.T) {
	for name, in := range map[string]string{
		"unknown field": `{"schema":"cubit.scan/v0","extra":1}`,
		"trailing data": `{"schema":"cubit.scan/v0"} {}`,
		"not json":      `nope`,
	} {
		if _, err := Decode(bytes.NewReader([]byte(in))); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func FuzzScanJSON(f *testing.F) {
	f.Add(exampleScan(f))
	f.Add([]byte(`{"schema":"cubit.scan/v0","generated_at":"2026-01-15T12:00:00Z","roots":[],"caps":{"max_workers":1,"depth_guard":64},"warnings":[]}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(`{"roots":[{"alias":"a","bytes":9223372036854775807,"duration_seconds":1e-7,"workspaces":null}]}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		once, err := Normalize(in)
		if err != nil {
			return
		}
		twice, err := Normalize(once)
		if err != nil {
			t.Fatalf("a normalized document does not decode: %v\n%s", err, once)
		}
		if !bytes.Equal(once, twice) {
			t.Fatalf("round trip is not byte-stable:\n%s\n%s", once, twice)
		}
	})
}
