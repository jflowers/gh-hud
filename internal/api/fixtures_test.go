package api

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	ghhudv1 "github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1"
)

var updateFixtures = flag.Bool("update-fixtures", false, "rewrite testdata/clients/*.binpb from the JSON")

// The clients' shared fixtures (testdata/clients/README.md) are snapshots
// written as protobuf JSON. The Android app's protobuf runtime can't read
// JSON, so both clients read the binary form, which this keeps in step:
// `make client-fixtures` rewrites it.
func TestClientFixturesAreCurrent(t *testing.T) {
	jsons, err := filepath.Glob("../../testdata/clients/*.json")
	if err != nil || len(jsons) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, path := range jsons {
		var s ghhudv1.Snapshot
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := protojson.Unmarshal(raw, &s); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		want, err := proto.MarshalOptions{Deterministic: true}.Marshal(&s)
		if err != nil {
			t.Fatal(err)
		}
		bin := strings.TrimSuffix(path, ".json") + ".binpb"
		if *updateFixtures {
			if err := os.WriteFile(bin, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(bin)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s is out of date with its JSON: run make client-fixtures", bin)
		}
	}
}
