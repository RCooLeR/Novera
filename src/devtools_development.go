//go:build !production

package main

// Development builds retain WebView developer tools for local diagnostics.
const devToolsEnabled = true
