package manualedit

import (
	"errors"
	"testing"
)

func TestSessionCumulativeLiveInsertedLimitAndReplacementCompaction(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxEditTextBytes = 4
	limits.MaxLiveInsertedBytes = 4
	session, err := NewSessionWithLimits(4, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 2, End: 2, Text: []byte("1234")}); err != nil {
		t.Fatal(err)
	}
	if got := session.table.liveInsertedBytes(); got != 4 {
		t.Fatalf("live inserted bytes = %d, want 4", got)
	}

	// Replacing all live added bytes must compact the dead append region rather
	// than retaining a second four-byte allocation in the current piece table.
	if err := session.ApplyEdit(Edit{Start: 2, End: 6, Text: []byte("WXYZ")}); err != nil {
		t.Fatal(err)
	}
	if got := session.table.liveInsertedBytes(); got != 4 {
		t.Fatalf("live inserted bytes after replacement = %d, want 4", got)
	}
	before := renderSessionForTest(t, session, "abcd")
	if err := session.ApplyEdit(Edit{Start: session.Size(), End: session.Size(), Text: []byte("!")}); !errors.Is(err, ErrLiveInsertedLimit) {
		t.Fatalf("error = %v, want ErrLiveInsertedLimit", err)
	}
	if got := renderSessionForTest(t, session, "abcd"); got != before {
		t.Fatalf("rejected edit changed content from %q to %q", before, got)
	}
}

func TestSessionEditCountAndHistoryDepthLimitsAreTransactional(t *testing.T) {
	t.Run("active edit count", func(t *testing.T) {
		limits := DefaultLimits()
		limits.MaxEditCount = 2
		session, err := NewSessionWithLimits(3, limits)
		if err != nil {
			t.Fatal(err)
		}
		if err := session.ApplyEdit(Edit{Start: 3, End: 3, Text: []byte("1")}); err != nil {
			t.Fatal(err)
		}
		if err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("2")}); err != nil {
			t.Fatal(err)
		}
		if err := session.ApplyEdit(Edit{Start: 5, End: 5, Text: []byte("3")}); !errors.Is(err, ErrEditCountLimit) {
			t.Fatalf("error = %v, want ErrEditCountLimit", err)
		}
		if session.EditCount() != 2 || renderSessionForTest(t, session, "abc") != "abc12" {
			t.Fatal("rejected count-limited edit mutated the session")
		}
	})

	t.Run("retained history depth", func(t *testing.T) {
		limits := DefaultLimits()
		limits.MaxEditCount = 3
		limits.MaxHistoryDepth = 2
		session, err := NewSessionWithLimits(3, limits)
		if err != nil {
			t.Fatal(err)
		}
		if err := session.ApplyEdit(Edit{Start: 3, End: 3, Text: []byte("1")}); err != nil {
			t.Fatal(err)
		}
		if err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("2")}); err != nil {
			t.Fatal(err)
		}
		if err := session.ApplyEdit(Edit{Start: 5, End: 5, Text: []byte("3")}); !errors.Is(err, ErrHistoryDepthLimit) {
			t.Fatalf("error = %v, want ErrHistoryDepthLimit", err)
		}
	})
}

func TestSessionPieceCountLimitRejectsBeforeStateChange(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxPieceCount = 3
	session, err := NewSessionWithLimits(4, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 1, End: 1, Text: []byte("X")}); err != nil {
		t.Fatal(err)
	}
	if got := session.table.pieceCount(); got != 3 {
		t.Fatalf("piece count = %d, want 3", got)
	}
	if err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("Y")}); !errors.Is(err, ErrPieceCountLimit) {
		t.Fatalf("error = %v, want ErrPieceCountLimit", err)
	}
	if got := renderSessionForTest(t, session, "abcd"); got != "aXbcd" {
		t.Fatalf("rejected piece-limited edit produced %q", got)
	}
}

func TestSessionRetainedHistoryByteBoundary(t *testing.T) {
	edit := Edit{Start: 1, End: 1, Text: []byte("x")}
	exact, err := historyEditBytes(edit)
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultLimits()
	limits.MaxHistoryBytes = exact
	session, err := NewSessionWithLimits(3, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(edit); err != nil {
		t.Fatalf("exact history-byte boundary rejected: %v", err)
	}
	if err := session.ApplyEdit(Edit{Start: 2, End: 2, Text: []byte("y")}); !errors.Is(err, ErrHistoryBytesLimit) {
		t.Fatalf("error = %v, want ErrHistoryBytesLimit", err)
	}

	limits.MaxHistoryBytes = exact - 1
	tooSmall, err := NewSessionWithLimits(3, limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := tooSmall.ApplyEdit(edit); !errors.Is(err, ErrHistoryBytesLimit) {
		t.Fatalf("below-boundary error = %v, want ErrHistoryBytesLimit", err)
	}
}

func TestSessionTransientLimitRejectsBeforeCloningOrMutation(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxTransientBytes = sourceWriteBufferBytes
	session, err := NewSessionWithLimits(3, limits)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte("x")
	err = session.ApplyEdit(Edit{Start: 1, End: 1, Text: input})
	if !errors.Is(err, ErrTransientMemoryLimit) {
		t.Fatalf("error = %v, want ErrTransientMemoryLimit", err)
	}
	input[0] = 'z'
	if session.EditCount() != 0 || session.CanUndo() || session.CanRedo() {
		t.Fatal("transient-limited edit changed history")
	}
	if got := renderSessionForTest(t, session, "abc"); got != "abc" {
		t.Fatalf("transient-limited edit produced %q", got)
	}
}

func TestSessionRejectsInvalidLimitPolicies(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxPieceCount = 0
	if _, err := NewSessionWithLimits(1, limits); !errors.Is(err, ErrInvalidSessionLimits) {
		t.Fatalf("error = %v, want ErrInvalidSessionLimits", err)
	}
	if _, err := NewSessionWithLimits(-1, DefaultLimits()); err == nil {
		t.Fatal("negative source size was accepted")
	}
}

func TestRejectedOversizedEditPreservesRedoWithoutAllocatingSessionState(t *testing.T) {
	session := NewSession(3, 4)
	if err := session.ApplyEdit(Edit{Start: 3, End: 3, Text: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	if err := session.Undo(); err != nil {
		t.Fatal(err)
	}
	history := &session.history[1]
	redoText := &session.history[1].Text[0]

	if err := session.ApplyEdit(Edit{Start: 4, End: 4, Text: []byte("12345")}); !errors.Is(err, ErrInsertedTextTooLarge) {
		t.Fatalf("error = %v, want ErrInsertedTextTooLarge", err)
	}
	if history != &session.history[1] || redoText != &session.history[1].Text[0] {
		t.Fatal("fail-fast validation rebuilt or cloned retained redo history")
	}
	if !session.CanRedo() {
		t.Fatal("rejected edit discarded redo history")
	}
}
