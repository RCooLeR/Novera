package agent

import "testing"

func TestApplyPatchMultiHunk(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\n"
	// Two hunks: change line2, and change line4 (line numbers intentionally off
	// to prove content-anchored matching).
	patch := "" +
		"@@ -1,3 +1,3 @@\n" +
		" line1\n" +
		"-line2\n" +
		"+line2-changed\n" +
		" line3\n" +
		"@@ -100,3 +100,3 @@\n" +
		" line3\n" +
		"-line4\n" +
		"+line4-changed\n" +
		" line5\n"

	hunks, err := parseUnifiedHunks(patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(hunks))
	}
	got, err := applyHunks(content, hunks)
	if err != nil {
		t.Fatal(err)
	}
	want := "line1\nline2-changed\nline3\nline4-changed\nline5\n"
	if got != want {
		t.Errorf("apply mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestApplyPatchAmbiguousAndMissing(t *testing.T) {
	// Ambiguous: the anchor occurs twice.
	dupe := "x\nx\n"
	h, _ := parseUnifiedHunks("@@ -1 +1 @@\n-x\n+y\n")
	if _, err := applyHunks(dupe, h); err == nil {
		t.Error("expected ambiguous-match error")
	}
	// Missing: anchor not present.
	if _, err := applyHunks("nothing here\n", h); err == nil {
		t.Error("expected no-match error")
	}
}

func TestParseUnifiedHunksEmpty(t *testing.T) {
	if _, err := parseUnifiedHunks("not a diff\n"); err == nil {
		t.Error("expected error for a patch with no hunks")
	}
}
