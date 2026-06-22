// Package highlight defines syntax token data shared by lexers and renderers.
//
// A token is a byte range plus a visual kind such as keyword, string, comment,
// or function. The package is intentionally small so plugins and editorcore can
// exchange highlighting information without depending on UI widgets.
package highlight
