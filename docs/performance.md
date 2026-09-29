# Large-file performance evidence

Novera uses bounded renderer windows, but end-to-end cost still depends on the
operation, file layout, encoding, match count, filesystem cache, memory, and
free disk. Performance claims must be tied to repeatable benchmark evidence.

## 2026-09-20 measured improvement

The dependency/bug audit added a rolling-hash fallback for repeated near-matches
in ASCII-folded search. On Windows/amd64, i9-13900KF, Go 1.27.1, five runs with
`-benchtime=100ms -count=5` over a 256 KiB buffer and 1,024-byte near-matching
needle produced these medians:

| Direction | Before | After | Median allocations |
| --- | ---: | ---: | ---: |
| Forward | 86.704 ms | 0.261 ms | 0 |
| Backward | 79.607 ms | 0.262 ms | 0 |

This measures a synthetic in-memory pathological case, not general app or disk
speed. The baseline used the exact pre-edit source with the same toolchain.
See the [full analysis](project-analysis-2026-09-20.md#performance-evidence),
[raw before results](benchmarks/2026-09-20-folded-search-before.txt), and
[raw after results](benchmarks/2026-09-20-folded-search-after.txt).

Run the regression benchmark from `src/` with:

```powershell
go test -run '^$' -bench BenchmarkFoldedRepeatedPrefix -benchmem -benchtime=100ms -count=5 ./internal/bigfile/asciifold
```

## Benchmark tiers

The Go benchmark suite covers metadata/open and viewport reads, sparse line
index construction, forward search across chunk boundaries, staging many edits,
and rendering a staged save. Fixtures include ordinary short lines and a giant
single line. Benchmarks report allocations and processed bytes.

Run the stable suite from the Go module in `src/`. Starting at the repository
root:

```powershell
Push-Location src
go test -run '^$' -bench Benchmark -benchmem -count 5 `
  ./internal/bigfile/asciifold `
  ./internal/bigfile/document `
  ./internal/bigfile/lineindex `
  ./internal/bigfile/manualedit `
  ./internal/bigfile/search
Pop-Location
```

The scheduled/manual Performance workflow retains raw output and its commit,
runner image, Go version, architecture, and fixture definitions. Do not compare
results from different runner images as though they were the same baseline.

## Review budgets

Until dedicated stable hardware is available, these are relative review gates
against the last accepted run on the same runner image:

- median `ns/op` regression greater than 20% requires investigation;
- `B/op` or `allocs/op` regression greater than 10% requires investigation;
- processed-byte throughput regression greater than 20% requires investigation;
- any loss of cancellation/bound tests, or any operation that begins retaining
  data proportional to an otherwise windowed file, blocks acceptance.

An investigation may accept a regression only with a documented user-visible
benefit, before/after evidence, and an updated baseline reviewed in the pull
request. These relative budgets are not release performance guarantees. Before
publishing quantified claims, run a full matrix on named hardware over sparse
large files, pathological encodings, giant records/lines, many matches/edits,
cold and warm cache, and constrained free disk; record peak RSS, cancel latency,
disk amplification, and tail latency as well as throughput.
