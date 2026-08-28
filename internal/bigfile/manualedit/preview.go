package manualedit

import (
	"strings"

	"novera/internal/bigfile/document"
)

type PreviewKind string

const (
	PreviewInsert  PreviewKind = "insert"
	PreviewReplace PreviewKind = "replace"
	PreviewDelete  PreviewKind = "delete"
)

type VisiblePreview struct {
	LineIndex int
	Kind      PreviewKind
	Selected  bool
	Lines     []string
}

func BuildVisiblePreviews(lines []document.VisualLine, edits []Edit, selected int, maxLines int, maxRunes int) []VisiblePreview {
	if len(lines) == 0 || len(edits) == 0 {
		return nil
	}
	if maxLines <= 0 {
		maxLines = 3
	}
	if maxRunes <= 0 {
		maxRunes = 120
	}

	previews := make([]VisiblePreview, 0, len(edits))
	for idx, edit := range edits {
		lineIndex, ok := previewAnchor(lines, edit)
		if !ok {
			continue
		}
		kind := PreviewReplace
		switch {
		case len(edit.Text) == 0:
			kind = PreviewDelete
		case edit.Start == edit.End:
			kind = PreviewInsert
		}
		previews = append(previews, VisiblePreview{
			LineIndex: lineIndex,
			Kind:      kind,
			Selected:  idx == selected,
			Lines:     previewLines(kind, edit.Text, maxLines, maxRunes),
		})
	}
	return previews
}

func previewAnchor(lines []document.VisualLine, edit Edit) (int, bool) {
	start := edit.Start
	end := edit.End
	if end < start {
		end = start
	}
	for i, line := range lines {
		lineStart := line.Offset
		lineEnd := line.DisplayEndOffset
		if i+1 < len(lines) && lines[i+1].Offset > lineEnd {
			lineEnd = lines[i+1].Offset
		}
		if lineEnd < lineStart {
			lineEnd = lineStart
		}
		if start == end {
			if start >= lineStart && start <= lineEnd {
				return i, true
			}
			continue
		}
		if start < lineEnd && end > lineStart {
			return i, true
		}
	}
	return 0, false
}

func previewLines(kind PreviewKind, text []byte, maxLines int, maxRunes int) []string {
	switch kind {
	case PreviewDelete:
		return []string{"Delete staged text"}
	case PreviewInsert:
		return prefixPreviewLines("Insert", text, maxLines, maxRunes)
	default:
		return prefixPreviewLines("Replace", text, maxLines, maxRunes)
	}
}

func prefixPreviewLines(label string, text []byte, maxLines int, maxRunes int) []string {
	normalized := strings.ReplaceAll(string(text), "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	out := make([]string, 0, min(maxLines, 15)+1)
	i := 0
	for part := range strings.SplitSeq(normalized, "\n") {
		if i == maxLines {
			out = append(out, label+" ...")
			break
		}
		part = clipRunes(part, maxRunes)
		if i == 0 {
			out = append(out, label+": "+part)
		} else {
			out = append(out, "  "+part)
		}
		i++
	}
	return out
}

func clipRunes(text string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "..."
}
