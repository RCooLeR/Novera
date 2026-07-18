package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"
	"novera/internal/buildinfo"
)

// Shell exposes host/OS interactions that require the running application
// instance (native dialogs, opening external resources). It is intentionally
// thin — file logic lives in the workspace service.
type Shell struct {
	unsavedResources atomic.Bool
}

// SetUnsavedResources mirrors the renderer's aggregate dirty-resource state
// into the native host. The WindowClosing hook consults this value before the
// WebView is torn down, so an OS title-bar close cannot bypass the renderer's
// normal save/discard checks.
func (s *Shell) SetUnsavedResources(unsaved bool) {
	s.unsavedResources.Store(unsaved)
}

func (s *Shell) hasUnsavedResources() bool {
	return s != nil && s.unsavedResources.Load()
}

// BuildInfo returns the canonical, non-secret identity embedded in the running
// executable so users and support can identify the exact build in About.
func (s *Shell) BuildInfo() buildinfo.Info {
	return buildinfo.Current()
}

// SelectFolder opens a native directory picker and returns the chosen absolute
// path. An empty string means the user cancelled.
func (s *Shell) SelectFolder() (string, error) {
	dir, err := application.Get().Dialog.OpenFile().
		CanChooseDirectories(true).
		CanChooseFiles(false).
		CanCreateDirectories(true).
		SetTitle("Open Folder").
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return dir, nil
}

// SaveTextFile opens a native Save dialog seeded with suggestedName and writes
// content to the chosen path. Returns the path, or "" if the user cancelled.
func (s *Shell) SaveTextFile(suggestedName, content string) (string, error) {
	path, err := application.Get().Dialog.SaveFile().
		SetFilename(suggestedName).
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if err := writeTextAtomic(path, []byte(content)); err != nil {
		return "", err
	}
	return path, nil
}

// writeTextAtomic stages a user-selected text export beside its destination,
// syncs the complete bytes, and publishes only after every write succeeds. A
// failed save therefore leaves an existing destination intact. When replacing
// a regular file, retain its permission bits instead of silently resetting it.
func writeTextAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	perm := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular destination %q", path)
		}
		perm = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".novera-export-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	closed = true
	if err := replaceExportFile(tmp, path); err != nil {
		return err
	}
	return syncExportDirectory(dir, path)
}

// OpenExternal opens a web/mail URL in the OS default handler. Only http(s) and
// mailto are allowed; file/UNC and other schemes are rejected so a crafted
// target can't trigger SMB/NTLM auth or launch arbitrary local handlers.
func (s *Shell) OpenExternal(target string) error {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "\\\\") || strings.HasPrefix(target, "//") {
		return fmt.Errorf("refusing to open UNC path")
	}
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return application.Get().Browser.OpenURL(target)
	case "mailto":
		// Rebuild the mailto from scratch with only safe headers so params like
		// attach/attachment (which some legacy handlers honour to pre-attach a
		// local file) can't ride along.
		return application.Get().Browser.OpenURL(safeMailto(u))
	default:
		return fmt.Errorf("refusing to open scheme %q", u.Scheme)
	}
}

// safeMailto reconstructs a mailto: URL keeping only the recipient(s) and an
// allowlist of headers (subject, body, cc, bcc); every other parameter —
// notably attach/attachment — is dropped.
func safeMailto(u *url.URL) string {
	out := &url.URL{Scheme: "mailto", Opaque: u.Opaque}
	in := u.Query()
	safe := url.Values{}
	for _, k := range []string{"subject", "body", "cc", "bcc"} {
		if v := in.Get(k); v != "" {
			safe.Set(k, v)
		}
	}
	if enc := safe.Encode(); enc != "" {
		out.RawQuery = enc
	}
	return out.String()
}
