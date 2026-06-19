package agent

import (
	"fmt"
	"strings"
)

// patchHunk is one diff hunk reduced to the exact text it expects to find
// (`before` = context + removed lines) and the text to put in its place
// (`after` = context + added lines).
type patchHunk struct {
	before string
	after  string
}

// parseUnifiedHunks parses the hunks of a unified diff body. It ignores the
// ---/+++ file headers (the tool takes the path separately) and the @@ line
// numbers (models routinely get them wrong); each hunk is reduced to before/
// after text so the applier can anchor by content instead of by offset.
func parseUnifiedHunks(diff string) ([]patchHunk, error) {
	lines := strings.Split(diff, "\n")
	// A trailing newline in the patch yields a spurious empty final element;
	// drop one so it isn't mistaken for a blank context line on the last hunk.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var hunks []patchHunk
	var before, after []string
	inHunk := false
	flush := func() {
		if inHunk {
			hunks = append(hunks, patchHunk{before: strings.Join(before, "\n"), after: strings.Join(after, "\n")})
		}
		before, after = nil, nil
	}
	for _, ln := range lines {
		switch {
		case strings.HasPrefix(ln, "@@"):
			flush()
			inHunk = true
		case strings.HasPrefix(ln, "diff ") || strings.HasPrefix(ln, "index ") ||
			strings.HasPrefix(ln, "---") || strings.HasPrefix(ln, "+++"):
			// file headers — ignore
		case strings.HasPrefix(ln, "\\"):
			// "\ No newline at end of file" — ignore
		case !inHunk:
			// preamble before the first hunk — ignore
		case strings.HasPrefix(ln, "+"):
			after = append(after, ln[1:])
		case strings.HasPrefix(ln, "-"):
			before = append(before, ln[1:])
		case strings.HasPrefix(ln, " "):
			ctx := ln[1:]
			before = append(before, ctx)
			after = append(after, ctx)
		case ln == "":
			// A blank context line whose leading space was dropped (common in
			// model-produced diffs): treat as empty context on both sides.
			before = append(before, "")
			after = append(after, "")
		default:
			// Unexpected prefix; be lenient and treat the whole line as context.
			before = append(before, ln)
			after = append(after, ln)
		}
	}
	flush()
	if len(hunks) == 0 {
		return nil, fmt.Errorf("no @@ hunks found in the patch")
	}
	return hunks, nil
}

// applyHunks applies hunks to content sequentially. Each hunk's `before` text
// must occur exactly once in the current content (anchoring by content, not line
// numbers); it is replaced by `after`. Later hunks see the result of earlier
// ones, so overlapping edits still resolve in order.
func applyHunks(content string, hunks []patchHunk) (string, error) {
	for i, h := range hunks {
		if strings.TrimSpace(h.before) == "" && h.before == "" {
			return "", fmt.Errorf("hunk %d has no context or removed lines to anchor to — include surrounding context", i+1)
		}
		n := strings.Count(content, h.before)
		if n == 0 {
			return "", fmt.Errorf("hunk %d did not match the file (its context/removed lines were not found) — the file may have changed; re-read it", i+1)
		}
		if n > 1 {
			return "", fmt.Errorf("hunk %d is ambiguous (%d matches) — include more surrounding context", i+1, n)
		}
		content = strings.Replace(content, h.before, h.after, 1)
	}
	return content, nil
}
