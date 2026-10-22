package artists

import (
	"encoding/json"
	"testing"
)

// roundTrip mimics what the store persists and reads back.
func roundTrip(t *testing.T, a AppliedCatalog) AppliedCatalog {
	t.Helper()
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	var out AppliedCatalog
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMergeAppliedReplacesOnlyListedArtists(t *testing.T) {
	prev := roundTrip(t, FingerprintCatalog(diffCatalog(diffArtist("a", "id1"), diffArtist("b", "id2"))))
	edited := diffCatalog(diffArtist("a", "id1", "id9"), diffArtist("b", "id2", "id8"))
	used := FingerprintCatalog(edited)

	merged := MergeApplied(prev, used, []string{"a"})

	changes := DiffCatalog(merged, used)
	if len(changes) != 1 || changes[0].Slug != "b" {
		t.Fatalf("after merging a, only b must still differ: %+v", changes)
	}
	if merged.Digest == prev.Digest || merged.Digest == used.Digest {
		t.Fatalf("merged digest must be recomputed: %s", merged.Digest)
	}
	// Merging every artist yields exactly the used catalog's digest.
	all := MergeApplied(merged, used, []string{"a", "b"})
	if all.Digest != used.Digest {
		t.Fatalf("full merge digest = %s, want %s", all.Digest, used.Digest)
	}
}

func TestMergeAppliedAddsAndDropsArtists(t *testing.T) {
	prev := roundTrip(t, FingerprintCatalog(diffCatalog(diffArtist("a", "id1"), diffArtist("b", "id2"))))
	used := FingerprintCatalog(diffCatalog(diffArtist("a", "id1"), diffArtist("c", "id3")))

	merged := MergeApplied(prev, used, []string{"b", "c"})
	if merged.Digest != used.Digest {
		t.Fatalf("dropping b and adding c must match used: %+v", DiffCatalog(merged, used))
	}
}

func TestMergeReconciledKeepsMetadataAndRelationships(t *testing.T) {
	prev := roundTrip(t, FingerprintCatalog(diffCatalog(diffArtist("a", "id1"), diffArtist("b", "id2"))))
	edited := diffCatalog(diffArtist("a", "id1", "id9"), diffArtist("b", "id2"))
	edited.Artists[0].Notes = "edited note"
	used := FingerprintCatalog(edited)

	merged, ok := MergeReconciled(prev, used, "a")
	if !ok {
		t.Fatal("merge refused")
	}
	changes := DiffCatalog(merged, used)
	if len(changes) != 1 || changes[0].Slug != "a" || len(changes[0].Kinds) != 1 || changes[0].Kinds[0] != ChangeMetadata {
		t.Fatalf("reconcile must apply the Spotify state but leave metadata pending: %+v", changes)
	}
}

func TestMergeReconciledRefusesUnknownArtist(t *testing.T) {
	prev := roundTrip(t, FingerprintCatalog(diffCatalog(diffArtist("a", "id1"))))
	used := FingerprintCatalog(diffCatalog(diffArtist("a", "id1"), diffArtist("c", "id3")))
	if _, ok := MergeReconciled(prev, used, "c"); ok {
		t.Fatal("an artist without an applied entry has no metadata baseline; merge must be refused")
	}
	if _, ok := MergeReconciled(prev, used, "zz"); ok {
		t.Fatal("an artist missing from the used catalog must be refused")
	}
}
