package gitsvc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"
)

type fixedRoot string

func (r fixedRoot) Root() string { return string(r) }

type singleUseRoot struct {
	t     *testing.T
	root  string
	calls int
}

func (r *singleUseRoot) Root() string {
	r.t.Helper()
	r.calls++
	if r.calls > 1 {
		r.t.Fatalf("workspace root was recaptured during one operation (%d calls)", r.calls)
	}
	return r.root
}

func helperGitCommand(ctx context.Context, _ string, args ...string) *exec.Cmd {
	helperArgs := append([]string{"-test.run=^TestGitHelperProcess$", "--"}, args...)
	return exec.CommandContext(ctx, os.Args[0], helperArgs...)
}

func testGitService(t *testing.T) *Service {
	t.Helper()
	t.Setenv("NOVERA_GIT_HELPER", "1")
	return &Service{roots: fixedRoot(t.TempDir()), command: helperGitCommand}
}

func TestStatusPropagatesGitStatusFailure(t *testing.T) {
	svc := testGitService(t)
	t.Setenv("NOVERA_GIT_STATUS_MODE", "fail")

	status, err := svc.Status()
	if err == nil || !strings.Contains(err.Error(), "read git status") {
		t.Fatalf("Status error = %v, want explicit status failure", err)
	}
	if status.Available {
		t.Fatal("a failed status command must not be reported as an available clean repository")
	}
}

func TestStatusDistinguishesProbeFailureFromNonRepository(t *testing.T) {
	t.Run("operational failure", func(t *testing.T) {
		svc := testGitService(t)
		t.Setenv("NOVERA_GIT_PROBE_MODE", "fail")
		_, err := svc.Status()
		if err == nil || !strings.Contains(err.Error(), "probe git repository") {
			t.Fatalf("Status error = %v, want operational probe failure", err)
		}
	})

	t.Run("not a repository", func(t *testing.T) {
		svc := testGitService(t)
		t.Setenv("NOVERA_GIT_PROBE_MODE", "not-repo")
		status, err := svc.Status()
		if err != nil || status.Available || !strings.Contains(status.Message, "not a Git repository") {
			t.Fatalf("Status = (%+v, %v), want unavailable non-repository", status, err)
		}
	})
}

func TestMutationPinsOneWorkspaceRoot(t *testing.T) {
	t.Setenv("NOVERA_GIT_HELPER", "1")
	roots := &singleUseRoot{t: t, root: t.TempDir()}
	svc := &Service{roots: roots, command: helperGitCommand}

	status, err := svc.StageAll()
	if err != nil {
		t.Fatalf("StageAll: %v", err)
	}
	if !status.Available || roots.calls != 1 {
		t.Fatalf("status available=%v root calls=%d, want true and 1", status.Available, roots.calls)
	}
}

func TestStatusRejectsOversizedPorcelainOutput(t *testing.T) {
	svc := testGitService(t)
	t.Setenv("NOVERA_GIT_STATUS_MODE", "huge")

	_, err := svc.Status()
	if !errors.Is(err, errGitOutputTooLarge) {
		t.Fatalf("Status error = %v, want errGitOutputTooLarge", err)
	}
}

func TestParseStatusCapsChangeAmplification(t *testing.T) {
	var raw strings.Builder
	raw.Grow((maxStatusChanges + 1) * 5)
	for i := 0; i <= maxStatusChanges; i++ {
		raw.WriteString("?? x\x00")
	}

	changes, _, err := parseStatusZ(raw.String())
	if !errors.Is(err, errGitOutputTooLarge) || changes != nil {
		t.Fatalf("parseStatusZ = (%d changes, %v), want nil and errGitOutputTooLarge", len(changes), err)
	}
}

func TestUnifiedDiffIsCappedDuringAcquisition(t *testing.T) {
	svc := testGitService(t)
	t.Setenv("NOVERA_GIT_DIFF_MODE", "huge")

	out, err := svc.UnifiedDiff("")
	if err != nil {
		t.Fatalf("UnifiedDiff: %v", err)
	}
	if !strings.Contains(out, "diff truncated") {
		t.Fatalf("missing truncation marker (length %d)", len(out))
	}
	if len(out) > maxDiffBytes+64 {
		t.Fatalf("diff length = %d, want bounded near %d", len(out), maxDiffBytes)
	}
	if !utf8.ValidString(out) {
		t.Fatal("truncated diff is not valid UTF-8")
	}
}

func TestUnifiedDiffSupportsUnbornRepository(t *testing.T) {
	svc := testGitService(t)
	t.Setenv("NOVERA_GIT_HEAD_MODE", "unborn")

	out, err := svc.UnifiedDiff("")
	if err != nil {
		t.Fatalf("UnifiedDiff: %v", err)
	}
	if out != "unborn cached diff\n" {
		t.Fatalf("UnifiedDiff = %q, want unborn cached diff", out)
	}
}

func TestCommitPropagatesStatFailure(t *testing.T) {
	svc := testGitService(t)
	t.Setenv("NOVERA_GIT_STAT_MODE", "fail")

	result, err := svc.Commit("subject", "")
	if err == nil || !strings.Contains(err.Error(), "inspect staged changes") {
		t.Fatalf("Commit error = %v, want staged-stat failure", err)
	}
	if result.Message == "No staged changes to commit." {
		t.Fatal("a failed stat command masqueraded as an empty index")
	}
}

func TestCommitPreservesConfirmedSuccessWhenRefreshFails(t *testing.T) {
	svc := testGitService(t)
	t.Setenv("NOVERA_GIT_HEAD_MODE", "fail")

	result, err := svc.Commit("subject", "")
	if err != nil {
		t.Fatalf("Commit returned an ambiguous error after success: %v", err)
	}
	if !result.Committed || result.RefreshError == "" || !strings.Contains(result.Message, "succeeded") {
		t.Fatalf("Commit result = %+v, want confirmed success with refresh diagnostic", result)
	}
}

func TestGitErrorOutputIsBounded(t *testing.T) {
	svc := testGitService(t)

	_, err := svc.git(statusTimeout, "emit-stderr")
	if err == nil {
		t.Fatal("git error was suppressed")
	}
	if len(err.Error()) > maxGitStderr+64 {
		t.Fatalf("stderr error length = %d, want bounded near %d", len(err.Error()), maxGitStderr)
	}
	if !strings.Contains(err.Error(), "error output truncated") {
		t.Fatal("bounded stderr did not disclose truncation")
	}
}

func TestGitErrorOutputRepairsTruncatedUTF8(t *testing.T) {
	svc := testGitService(t)

	_, err := svc.git(statusTimeout, "emit-stderr-utf8")
	if err == nil {
		t.Fatal("git error was suppressed")
	}
	if !utf8.ValidString(err.Error()) {
		t.Fatal("bounded git error is not valid UTF-8")
	}
}

func TestReadFileLimitedRejectsLargeWorkingFile(t *testing.T) {
	path := t.TempDir() + string(os.PathSeparator) + "large.txt"
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxDiffBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}

	data, tooLarge, err := readFileLimited(path, maxDiffBytes)
	if err != nil {
		t.Fatalf("readFileLimited: %v", err)
	}
	if !tooLarge || data != nil {
		t.Fatalf("readFileLimited = (%d bytes, %v), want nil and tooLarge", len(data), tooLarge)
	}
}

func TestGitShowDistinguishesMissingObjectFromFailure(t *testing.T) {
	svc := testGitService(t)

	t.Setenv("NOVERA_GIT_SHOW_MODE", "missing")
	_, exists, err := svc.gitShow(svc.root(), "HEAD:./missing.txt")
	if err != nil || exists {
		t.Fatalf("missing git object = (exists %v, err %v), want false and nil", exists, err)
	}

	t.Setenv("NOVERA_GIT_SHOW_MODE", "fail")
	_, exists, err = svc.gitShow(svc.root(), "HEAD:./file.txt")
	if err == nil || exists {
		t.Fatalf("failed git show = (exists %v, err %v), want false and error", exists, err)
	}
}

func TestCappedBufferDiscardsExcessWithoutShortWrite(t *testing.T) {
	buf := newCappedBuffer(4)
	n, err := buf.Write([]byte("abcdefgh"))
	if err != nil || n != 8 {
		t.Fatalf("Write = (%d, %v), want (8, nil)", n, err)
	}
	if got := buf.String(); got != "abcd" || !buf.truncated {
		t.Fatalf("buffer = %q truncated=%v", got, buf.truncated)
	}
}

func TestTrimTruncatedUTF8(t *testing.T) {
	if got := trimTruncatedUTF8("ascii"); got != "ascii" {
		t.Fatalf("valid ASCII changed to %q", got)
	}
	partial := string(append([]byte("ok"), 0xE2, 0x80))
	if got := trimTruncatedUTF8(partial); got != "ok" {
		t.Fatalf("partial rune trimmed to %q, want %q", got, "ok")
	}
	if got := trimTruncatedUTF8(string([]byte{'a', 0xff, 'b'})); !utf8.ValidString(got) {
		t.Fatalf("invalid input remained invalid: %q", got)
	}
}

// TestGitHelperProcess is executed in a subprocess by helperGitCommand.
func TestGitHelperProcess(t *testing.T) {
	if os.Getenv("NOVERA_GIT_HELPER") != "1" {
		return
	}
	args := helperProcessArgs(os.Args)
	switch {
	case containsArg(args, "emit-stderr-utf8"):
		writeRepeated(os.Stderr, maxGitStderr-1)
		_, _ = os.Stderr.Write([]byte{0xE2, 0x80, 0xA6, 'x'})
		os.Exit(7)
	case containsArg(args, "emit-stderr"):
		writeRepeated(os.Stderr, maxGitStderr+4096)
		os.Exit(7)
	case containsArg(args, "status"):
		switch os.Getenv("NOVERA_GIT_STATUS_MODE") {
		case "fail":
			fmt.Fprint(os.Stderr, "simulated status failure")
			os.Exit(7)
		case "huge":
			writeRepeated(os.Stdout, maxGitOutput+4096)
			os.Exit(0)
		default:
			fmt.Fprint(os.Stdout, "## main\x00")
		}
	case containsArg(args, "diff"):
		if containsArg(args, "--stat") {
			if os.Getenv("NOVERA_GIT_STAT_MODE") == "fail" {
				fmt.Fprint(os.Stderr, "simulated stat failure")
				os.Exit(8)
			}
			fmt.Fprint(os.Stdout, "file.txt | 1 +")
			os.Exit(0)
		}
		if os.Getenv("NOVERA_GIT_DIFF_MODE") == "huge" {
			writeRepeated(os.Stdout, maxDiffBytes+4096)
			os.Exit(0)
		}
		if os.Getenv("NOVERA_GIT_HEAD_MODE") == "unborn" {
			if !containsArg(args, "--cached") {
				fmt.Fprint(os.Stderr, "unborn diff did not use --cached")
				os.Exit(12)
			}
			fmt.Fprintln(os.Stdout, "unborn cached diff")
			os.Exit(0)
		}
		fmt.Fprint(os.Stdout, "diff --git a/file b/file\n")
	case containsArg(args, "branch"):
		fmt.Fprintln(os.Stdout, "main")
	case containsArg(args, "rev-parse"):
		if containsArg(args, "--is-inside-work-tree") {
			switch os.Getenv("NOVERA_GIT_PROBE_MODE") {
			case "fail":
				fmt.Fprint(os.Stderr, "simulated executable failure")
				os.Exit(10)
			case "not-repo":
				fmt.Fprint(os.Stderr, "fatal: not a git repository")
				os.Exit(128)
			default:
				fmt.Fprintln(os.Stdout, "true")
			}
		} else if os.Getenv("NOVERA_GIT_HEAD_MODE") == "unborn" {
			os.Exit(1)
		} else if os.Getenv("NOVERA_GIT_HEAD_MODE") == "fail" {
			fmt.Fprint(os.Stderr, "simulated head refresh failure")
			os.Exit(11)
		} else {
			fmt.Fprintln(os.Stdout, "0123456789abcdef")
		}
	case containsArg(args, "show"):
		switch os.Getenv("NOVERA_GIT_SHOW_MODE") {
		case "missing":
			fmt.Fprint(os.Stderr, "fatal: path 'missing.txt' does not exist in 'HEAD'")
			os.Exit(128)
		case "fail":
			fmt.Fprint(os.Stderr, "simulated object database failure")
			os.Exit(13)
		default:
			fmt.Fprint(os.Stdout, "content")
		}
	case containsArg(args, "commit"):
		fmt.Fprintln(os.Stdout, "committed")
	case containsArg(args, "add"), containsArg(args, "restore"):
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unexpected helper args: %q", args)
		os.Exit(9)
	}
	os.Exit(0)
}

func helperProcessArgs(args []string) []string {
	for i, arg := range args {
		if arg == "--" {
			return args[i+1:]
		}
	}
	return nil
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func writeRepeated(dst *os.File, total int) {
	chunk := bytes.Repeat([]byte("x"), 4096)
	for total > 0 {
		n := len(chunk)
		if n > total {
			n = total
		}
		_, _ = dst.Write(chunk[:n])
		total -= n
	}
}
