package reason

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func schemaEnum(t *testing.T, def string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "schemas", "cubit.scan.v0.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Defs map[string]struct {
			Enum []string `json:"enum"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s.Defs[def].Enum
}

func TestEnumsMatchTheSchema(t *testing.T) {
	for def, got := range map[string][]string{"skipReason": Skip, "errorReason": Error, "warningReason": Warning} {
		want := append([]string(nil), schemaEnum(t, def)...)
		have := append([]string(nil), got...)
		sort.Strings(want)
		sort.Strings(have)
		if !reflect.DeepEqual(want, have) {
			t.Errorf("%s: schema %v, code %v", def, want, have)
		}
	}
}

func TestMakesPartialMatchesTheContract(t *testing.T) {
	for _, r := range Skip {
		want := r == "permission_denied" || r == "io_error" || r == "depth_exceeded"
		if MakesPartial(r) != want {
			t.Errorf("%s: MakesPartial %v", r, MakesPartial(r))
		}
	}
}
