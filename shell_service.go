package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// Shell exposes host/OS interactions that require the running application
// instance (native dialogs, opening external resources). It is intentionally
// thin — file logic lives in the workspace service.
type Shell struct{}

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
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
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
