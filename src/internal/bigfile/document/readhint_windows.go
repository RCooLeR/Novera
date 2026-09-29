//go:build windows

package document

import (
	"os"

	"golang.org/x/sys/windows"
)

// applyReadHint reopens the file with FILE_FLAG_SEQUENTIAL_SCAN so Windows uses a
// larger read-ahead window for the front-to-back streaming passes (line index,
// search, analyze, export). ReadAt is positional (it ignores the file offset), so
// swapping in a fresh handle is safe. On any error it keeps the original handle.
func applyReadHint(f *os.File, path string) *os.File {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return f
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_SEQUENTIAL_SCAN,
		0,
	)
	if err != nil {
		return f
	}
	nf := os.NewFile(uintptr(h), path)
	if nf == nil {
		_ = windows.CloseHandle(h)
		return f
	}
	_ = f.Close()
	return nf
}
