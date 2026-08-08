package modelchannel

import "testing"

type testAdapter struct {
	id          string
	modelID     string
	displayName string
	legacyID    string
}

func TestResolveAdapterIndexMatchesDisplayName(t *testing.T) {
	adapters := []testAdapter{
		{id: "hash-a", modelID: "composer-2.5", displayName: "Local Composer", legacyID: "legacy-a"},
		{id: "hash-b", modelID: "gpt-4o", displayName: "GPT", legacyID: "legacy-b"},
	}
	resolve := func(requested string) (int, bool) {
		return ResolveAdapterIndex(
			adapters,
			requested,
			func(adapter testAdapter) string { return adapter.id },
			func(adapter testAdapter) string { return adapter.modelID },
			func(adapter testAdapter) string { return adapter.legacyID },
			func(adapter testAdapter) string { return adapter.displayName },
		)
	}

	cases := []struct {
		name      string
		requested string
		wantIndex int
		wantOK    bool
	}{
		{name: "channel hash", requested: "hash-b", wantIndex: 1, wantOK: true},
		{name: "provider model id", requested: "composer-2.5", wantIndex: 0, wantOK: true},
		{name: "display name", requested: "Local Composer", wantIndex: 0, wantOK: true},
		{name: "legacy hash", requested: "legacy-b", wantIndex: 1, wantOK: true},
		{name: "meta default", requested: "default", wantIndex: 0, wantOK: true},
		{name: "unknown", requested: "missing", wantIndex: -1, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotIndex, ok := resolve(tc.requested)
			if ok != tc.wantOK || gotIndex != tc.wantIndex {
				t.Fatalf("ResolveAdapterIndex(%q)=(%d,%v) want (%d,%v)", tc.requested, gotIndex, ok, tc.wantIndex, tc.wantOK)
			}
		})
	}
}

func TestResolveAdapterIndexRejectsAmbiguousDisplayName(t *testing.T) {
	adapters := []testAdapter{
		{id: "hash-a", modelID: "model-a", displayName: "Shared"},
		{id: "hash-b", modelID: "model-b", displayName: "Shared"},
	}
	_, ok := ResolveAdapterIndex(
		adapters,
		"Shared",
		func(adapter testAdapter) string { return adapter.id },
		func(adapter testAdapter) string { return adapter.modelID },
		func(adapter testAdapter) string { return adapter.displayName },
	)
	if ok {
		t.Fatal("expected ambiguous display name to be unresolvable")
	}
}
