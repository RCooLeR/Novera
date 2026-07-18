//go:build production

package main

// Packaged builds expose privileged Wails bridge services, so they must not
// ship with an interactive WebView debugging surface enabled.
const devToolsEnabled = false
