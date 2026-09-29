package document

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novera/internal/bigfile/lineindex"
	"novera/internal/bigfile/regularfile"
	"novera/internal/bigfile/settings"
)

func TestReadRangeNeverReturnsStaleCachedBytesAfterDetectedSourceChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("old bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	if got, err := doc.ReadRange(0, doc.Size()); err != nil || string(got) != "old bytes" {
		t.Fatalf("initial read = %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	changedTime := doc.OriginalFileState().ModTime.Add(2 * time.Second)
	if err := os.Chtimes(path, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}

	if got, err := doc.ReadRange(0, doc.Size()); !errors.Is(err, ErrSourceChanged) || got != nil {
		t.Fatalf("cached read after source change = %q, %v; want ErrSourceChanged and no bytes", got, err)
	}
	if doc.cache.bytes != 0 || len(doc.cache.chunks) != 0 {
		t.Fatalf("stale cache was not cleared: bytes=%d chunks=%d", doc.cache.bytes, len(doc.cache.chunks))
	}
}

func TestDeferredIndexCacheHydrationKeepsPointerStable(t *testing.T) {
	path, size, _, _ := prepareDeferredIndexCache(t)

	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	stable := doc.idx

	if err := doc.StartIndexing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if doc.idx != stable {
		t.Fatal("deferred hydration replaced the stable Index pointer")
	}
	if progress := doc.IndexProgress(); !progress.Done || progress.Lines != 2 || progress.Bytes != size {
		t.Fatalf("progress = %#v, want hydrated cached snapshot", progress)
	}
}

func TestDeferredIndexCacheHydrationRejectsChangedSourceAfterFingerprint(t *testing.T) {
	path, _, info, hash := prepareDeferredIndexCache(t)

	doc, err := OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	stable := doc.idx
	doc.indexSampleHashMu.Lock()
	doc.indexSampleHash = hash
	doc.indexSampleHashMu.Unlock()

	mutator, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.WriteAt([]byte("ONE"), 0); err != nil {
		_ = mutator.Close()
		t.Fatal(err)
	}
	if err := mutator.Close(); err != nil {
		t.Fatal(err)
	}
	changedTime := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}

	if doc.hydrateIndexCache(context.Background()) {
		t.Fatal("cache hydration accepted a source changed after its cached fingerprint")
	}
	if doc.idx != stable {
		t.Fatal("rejected hydration replaced the stable Index pointer")
	}
	if progress := doc.IndexProgress(); progress.Done {
		t.Fatalf("rejected cache marked the index complete: %#v", progress)
	}
}

func TestValidateExternalModificationReadRejectsPathExchange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.txt")
	held := filepath.Join(dir, "source.held")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := regularfile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	openedInfo, err := reopened.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, held); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := validateExternalModificationRead(path, reopened, openedInfo); err == nil {
		t.Fatal("external-modification validation accepted a replaced pathname")
	}
}

func prepareDeferredIndexCache(t *testing.T) (string, int64, os.FileInfo, string) {
	t.Helper()
	oldPersist := persistentIndexCacheEnabled()
	SetPersistentIndexCache(true)
	t.Cleanup(func() { SetPersistentIndexCache(oldPersist) })
	t.Setenv(settings.ConfigDirEnv, t.TempDir())
	t.Setenv(settings.LegacyConfigDirEnv, "")

	path := filepath.Join(t.TempDir(), "huge-cached.txt")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("one\ntwo\n"), 0); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	size := int64(synchronousIndexCacheMaxSourceSize + 1)
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	hash, err := computeIndexSourceHashContext(context.Background(), file, size)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	every := lineindex.EveryLinesForSize(size)
	cached := lineindex.New(every)
	if err := cached.RestoreSnapshot(lineindex.Snapshot{
		EveryLines: every,
		Entries:    []lineindex.Entry{{Line: 1, Offset: 0}},
		Lines:      2,
		Bytes:      size,
		Done:       true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := saveIndexCache(path, size, info.ModTime(), hash, cached); err != nil {
		t.Fatal(err)
	}
	return path, size, info, hash
}
