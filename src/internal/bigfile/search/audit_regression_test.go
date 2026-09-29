package search

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestWholeWordPunctuationAcrossUTF8Seams(t *testing.T) {
	for _, delimiter := range []string{"\u00a0", "\u20ac", "\U0001f600"} {
		for chunkSize := 7; chunkSize <= 13; chunkSize++ {
			for padding := 0; padding < chunkSize; padding++ {
				t.Run(fmt.Sprintf("%U/chunk=%d/padding=%d", []rune(delimiter)[0], chunkSize, padding), func(t *testing.T) {
					text := strings.Repeat(".", padding) + "cat" + delimiter + "cat" + delimiter + "cat"
					want := []int64{int64(padding), int64(padding + 3 + len(delimiter)), int64(padding + 6 + 2*len(delimiter))}
					for _, backward := range []bool{false, true} {
						find := FindPlain
						if backward {
							find = FindPlainBackward
						}
						var got []int64
						err := find(context.Background(), memReaderAt{data: []byte(text)}, []byte("cat"), PlainOptions{ChunkSize: chunkSize, WholeWord: true}, func(m Match) error {
							got = append(got, m.Offset)
							return nil
						})
						if backward {
							slices.Reverse(got)
						}
						if err != nil || !slices.Equal(got, want) {
							t.Fatalf("backward=%v: offsets=%v err=%v, want %v", backward, got, err, want)
						}
					}
				})
			}
		}
	}
}

func TestSearchCancellationDuringDenseChunk(t *testing.T) {
	reader := memReaderAt{data: bytes.Repeat([]byte("a"), 4096)}
	re, err := CompileRegexpForTesting([]byte("a"), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"plain-forward", "plain-backward", "regex-forward", "regex-backward"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			emit := func(Match) error {
				calls++
				cancel()
				return nil
			}
			var err error
			switch mode {
			case "plain-forward":
				err = FindPlain(ctx, reader, []byte("a"), PlainOptions{ChunkSize: len(reader.data)}, emit)
			case "plain-backward":
				err = FindPlainBackward(ctx, reader, []byte("a"), PlainOptions{ChunkSize: len(reader.data)}, emit)
			case "regex-forward":
				err = FindRegexp(ctx, reader, re, RegexOptions{ChunkSize: len(reader.data)}, emit)
			case "regex-backward":
				err = FindRegexpBackward(ctx, reader, re, RegexOptions{ChunkSize: len(reader.data)}, emit)
			}
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("calls=%d err=%v; want one callback and context.Canceled", calls, err)
			}
		})
	}
}

func TestFindRegexpNonOverlappingMatchesAreChunkInvariant(t *testing.T) {
	for _, pattern := range []string{"aa", "a{2,3}", "aba"} {
		for _, data := range []string{strings.Repeat("a", 31), strings.Repeat("aba", 11)} {
			re, err := CompileRegexpForTesting([]byte(pattern), false)
			if err != nil {
				t.Fatal(err)
			}
			for start := 0; start < 4; start++ {
				var want []Match
				for _, loc := range re.FindAllIndex([]byte(data[start:]), -1) {
					want = append(want, Match{Offset: int64(start + loc[0]), Length: loc[1] - loc[0]})
				}
				for chunk := 3; chunk <= 11; chunk++ {
					var got []Match
					err := FindRegexp(context.Background(), memReaderAt{data: []byte(data)}, re, RegexOptions{StartOffset: int64(start), ChunkSize: chunk, MaxMatchWindow: 3}, func(m Match) error {
						got = append(got, m)
						return nil
					})
					if err != nil || !slices.Equal(got, want) {
						t.Fatalf("pattern=%s chunk=%d start=%d: got=%v err=%v, want=%v", pattern, chunk, start, got, err, want)
					}
				}
			}
		}
	}
}
