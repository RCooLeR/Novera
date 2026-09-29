package document

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/search"
)

const testMiB = 1024 * 1024
const testSmallFileAutoLoadLimitBytes = 8 * 1024 * 1024

func TestLoadInMemoryBufferBudgetFittingLargeFileKeepsDocumentStreamingSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "budget-fitting.log")
	marker := []byte("needle-at-end\n")
	payloadSize := testSmallFileAutoLoadLimitBytes + 512*1024
	if err := writePatternFile(path, payloadSize, marker); err != nil {
		t.Fatal(err)
	}

	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	limit := int64(500 * testMiB)
	buf, err := LoadInMemoryBuffer(doc, limit)
	if err != nil {
		t.Fatal(err)
	}
	if buf.OriginalSize <= testSmallFileAutoLoadLimitBytes {
		t.Fatalf("fixture size = %d, want larger than small auto-load threshold", buf.OriginalSize)
	}
	if buf.EditableLimit != limit {
		t.Fatalf("EditableLimit = %d, want %d", buf.EditableLimit, limit)
	}
	if !strings.Contains(buf.Text, string(marker)) {
		t.Fatalf("in-memory buffer did not include marker near EOF")
	}

	// Mutating the normal editor buffer must not change the source-of-truth file.
	editedText := strings.Replace(buf.Text, string(marker), "edited-buffer-only\n", 1)
	if _, err := buf.EncodedBytes(editedText); err != nil {
		t.Fatal(err)
	}

	var offsets []int64
	err = search.FindPlain(context.Background(), doc, []byte(marker), search.PlainOptions{
		ChunkSize: 64 * 1024,
		MaxHits:   2,
	}, func(m search.Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 1 {
		t.Fatalf("streaming search offsets = %#v, want one source-file marker", offsets)
	}
	if offsets[0] < int64(payloadSize) {
		t.Fatalf("marker offset = %d, want near EOF after payload size %d", offsets[0], payloadSize)
	}
}

func TestLoadInMemoryWindowKeepsWholeFileStreamingSearchSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge-window.log")
	content := []byte("head needle\nslice needle\noutside needle\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	sliceStart := int64(len("head needle\n"))
	sliceEnd := sliceStart + int64(len("slice needle\n"))
	buf, err := LoadInMemoryWindow(doc, sliceStart, sliceEnd, sliceEnd-sliceStart)
	if err != nil {
		t.Fatal(err)
	}
	if buf.Text != "slice needle\n" {
		t.Fatalf("slice text = %q", buf.Text)
	}

	// Editing the active slice is staged separately; full-file tools keep using
	// the disk-backed document so matches outside and inside the slice remain
	// visible until an explicit safe output/finalize flow succeeds.
	editedSlice := strings.Replace(buf.Text, "needle", "mark", 1)
	if _, _, encoded, err := buf.EncodedReplacement(editedSlice); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(encoded), "needle") {
		t.Fatalf("edited slice replacement still contains search marker: %q", string(encoded))
	}

	var offsets []int64
	err = search.FindPlain(context.Background(), doc, []byte("needle"), search.PlainOptions{
		ChunkSize: 8,
		MaxHits:   10,
	}, func(m search.Match) error {
		offsets = append(offsets, m.Offset)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantOffsets := []int64{5, sliceStart + 6, int64(len("head needle\nslice needle\noutside "))}
	if len(offsets) != len(wantOffsets) {
		t.Fatalf("streaming search offsets = %#v, want %#v", offsets, wantOffsets)
	}
	for i := range wantOffsets {
		if offsets[i] != wantOffsets[i] {
			t.Fatalf("streaming search offsets = %#v, want %#v", offsets, wantOffsets)
		}
	}
}

func TestLoadInMemoryBufferWithinBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	buf, err := LoadInMemoryBuffer(doc, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if buf.Text != "hello\nworld\n" {
		t.Fatalf("Text = %q", buf.Text)
	}
	if buf.Encoding != "UTF-8" {
		t.Fatalf("Encoding = %q", buf.Encoding)
	}
	if !buf.CoversWholeFile() {
		t.Fatalf("CoversWholeFile = false, want true")
	}
}

func TestLoadInMemoryWindowReadsOnlyBoundedSlice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge-log.txt")
	content := []byte("0000\n1111\n2222\n3333\n4444\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	buf, err := LoadInMemoryWindow(doc, 5, 15, 10)
	if err != nil {
		t.Fatal(err)
	}
	if buf.CoversWholeFile() {
		t.Fatalf("CoversWholeFile = true, want false")
	}
	if buf.OriginalSize != int64(len(content)) {
		t.Fatalf("OriginalSize = %d, want %d", buf.OriginalSize, len(content))
	}
	if buf.WindowStart != 5 || buf.WindowEnd != 15 {
		t.Fatalf("window = [%d,%d), want [5,15)", buf.WindowStart, buf.WindowEnd)
	}
	if buf.Text != "1111\n2222\n" {
		t.Fatalf("Text = %q", buf.Text)
	}
}

func TestLoadInMemoryWindowReportsReadAndDecodeProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge-log.txt")
	if err := os.WriteFile(path, []byte("0000\n1111\n2222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	var stages []InMemoryLoadProgress
	buf, err := LoadInMemoryWindowContextWithOptions(context.Background(), doc, 5, 10, 16, InMemoryLoadOptions{
		Progress: func(progress InMemoryLoadProgress) {
			stages = append(stages, progress)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if buf.Text != "1111\n" {
		t.Fatalf("loaded text = %q, want bounded slice", buf.Text)
	}
	if len(stages) != 2 {
		t.Fatalf("stages = %#v, want read and decode", stages)
	}
	if stages[0].Stage != InMemoryLoadStageReading || stages[0].Start != 5 || stages[0].End != 10 || stages[0].Bytes != 5 {
		t.Fatalf("read stage = %#v, want bounded read metadata", stages[0])
	}
	if stages[1].Stage != InMemoryLoadStageDecoding || stages[1].Bytes != 5 || stages[1].EditableLimit != 16 {
		t.Fatalf("decode stage = %#v, want bounded decode metadata", stages[1])
	}
}

func TestLoadInMemoryWindowContextCanceledBeforeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge-log.txt")
	if err := os.WriteFile(path, []byte("0000\n1111\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = LoadInMemoryWindowContext(ctx, doc, 0, 5, 5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLoadInMemoryBufferContextCanceledBeforeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = LoadInMemoryBufferContext(ctx, doc, 1024)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestLoadInMemoryWindowRejectsOverBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge-log.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	_, err = LoadInMemoryWindow(doc, 1, 6, 4)
	if !errors.Is(err, ErrInMemoryBufferTooLarge) {
		t.Fatalf("err = %v, want ErrInMemoryBufferTooLarge", err)
	}
}

func TestInMemoryWindowEncodedReplacementReturnsSourceRange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge-log.txt")
	if err := os.WriteFile(path, []byte("0000\n1111\n2222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	buf, err := LoadInMemoryWindow(doc, 5, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	start, end, encoded, err := buf.EncodedReplacement("abcd\n")
	if err != nil {
		t.Fatal(err)
	}
	if start != 5 || end != 10 {
		t.Fatalf("range = [%d,%d), want [5,10)", start, end)
	}
	if string(encoded) != "abcd\n" {
		t.Fatalf("encoded = %q", encoded)
	}
}

func TestInMemoryWindowEncodedReplacementKeepsSourceEncoding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf16.txt")
	if err := os.WriteFile(path, utf16LEWithBOM("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	buf, err := LoadInMemoryWindow(doc, 2, 6, 16)
	if err != nil {
		t.Fatal(err)
	}
	if buf.Text != "he" {
		t.Fatalf("Text = %q, want he", buf.Text)
	}
	start, end, encoded, err := buf.EncodedReplacement("yo")
	if err != nil {
		t.Fatal(err)
	}
	if start != 2 || end != 6 {
		t.Fatalf("range = [%d,%d), want [2,6)", start, end)
	}
	if len(encoded) != 4 || encoded[0] != 'y' || encoded[1] != 0 || encoded[2] != 'o' || encoded[3] != 0 {
		t.Fatalf("encoded UTF-16LE bytes = % x", encoded)
	}
}

func TestInMemoryWindowWriteCopyRefusesPartialWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge-log.txt")
	if err := os.WriteFile(path, []byte("0000\n1111\n2222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	buf, err := LoadInMemoryWindow(doc, 5, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buf.WriteCopy(filepath.Join(dir, "out.txt"), "abcd\n"); err == nil {
		t.Fatal("WriteCopy succeeded for partial window; want error")
	}
}

func TestLoadInMemoryBufferRejectsOverBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	_, err = LoadInMemoryBuffer(doc, 5)
	if !errors.Is(err, ErrInMemoryBufferTooLarge) {
		t.Fatalf("err = %v, want ErrInMemoryBufferTooLarge", err)
	}
}

func TestLoadInMemoryBufferRejectsBinary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0x03}, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	_, err = LoadInMemoryBuffer(doc, 1024)
	if !errors.Is(err, ErrInMemoryBufferBinary) {
		t.Fatalf("err = %v, want ErrInMemoryBufferBinary", err)
	}
}

func TestInMemoryBufferWriteCopyPreservesSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	buf, err := LoadInMemoryBuffer(doc, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := buf.WriteCopy(path, "changed"); err == nil {
		t.Fatal("WriteCopy to source path succeeded; want error")
	}
	output := filepath.Join(dir, "source.edited.txt")
	written, err := buf.WriteCopy(output, "changed")
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len("changed")) {
		t.Fatalf("written = %d", written)
	}
	sourceBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceBytes) != "original" {
		t.Fatalf("source = %q, want original", string(sourceBytes))
	}
	outputBytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(outputBytes) != "changed" {
		t.Fatalf("output = %q", string(outputBytes))
	}
}

func TestInMemoryBufferWriteCopyContextCanceledBeforePublish(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	output := filepath.Join(dir, "source.edited.txt")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	buf, err := LoadInMemoryBuffer(doc, 1024)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := buf.WriteCopyContext(ctx, output, "changed"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output stat err = %v, want not exist", err)
	}
	sourceBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceBytes) != "original" {
		t.Fatalf("source = %q, want original", string(sourceBytes))
	}
}

func TestInMemoryBufferWriteCopyRefusesExistingOutputByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	output := filepath.Join(dir, "source.edited.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	buf, err := LoadInMemoryBuffer(doc, 1024)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := buf.WriteCopy(output, "changed"); !errors.Is(err, fileio.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Fatalf("existing output changed to %q", got)
	}
	if _, err := os.Stat(output + ".quarry.tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp stat err = %v, want not exist", err)
	}
}

func TestInMemoryBufferWriteCopyAllowsExplicitOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	output := filepath.Join(dir, "source.edited.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	buf, err := LoadInMemoryBuffer(doc, 1024)
	if err != nil {
		t.Fatal(err)
	}

	written, err := buf.WriteCopyWithOptions(output, "changed", WriteCopyOptions{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len("changed")) {
		t.Fatalf("written = %d", written)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "changed" {
		t.Fatalf("output = %q, want changed", got)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != "original" {
		t.Fatalf("source changed to %q", source)
	}
}

func TestInMemoryBufferWriteCopyUsesSourceMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file permission bits are not stable enough for this assertion")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	buf, err := LoadInMemoryBuffer(doc, 1024)
	if err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(dir, "source.edited.txt")
	if _, err := buf.WriteCopy(output, "changed"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %v, want 0600", got)
	}
}

func TestInMemoryBufferWriteCopyKeepsUTF16LEEncoding(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "utf16.txt")
	data := utf16LEWithBOM("hello")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	buf, err := LoadInMemoryBuffer(doc, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(buf.Encoding, "UTF-16LE") {
		t.Fatalf("Encoding = %q", buf.Encoding)
	}

	output := filepath.Join(dir, "utf16.edited.txt")
	if _, err := buf.WriteCopy(output, "bye"); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) < 2 || written[0] != 'b' || written[1] != 0 {
		t.Fatalf("output bytes do not look UTF-16LE encoded: % x", written)
	}
}

func utf16LEWithBOM(s string) []byte {
	encoded := utf16.Encode([]rune(s))
	out := []byte{0xFF, 0xFE}
	for _, r := range encoded {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

func writePatternFile(path string, payloadSize int, suffix []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	chunk := []byte("0123456789abcdef old_database row payload\n")
	written := 0
	for written < payloadSize {
		remaining := payloadSize - written
		part := chunk
		if remaining < len(part) {
			part = part[:remaining]
		}
		n, err := f.Write(part)
		written += n
		if err != nil {
			return err
		}
		if n != len(part) {
			return errors.New("short write")
		}
	}
	_, err = f.Write(suffix)
	return err
}
