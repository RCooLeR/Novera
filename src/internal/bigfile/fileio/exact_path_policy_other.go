//go:build !windows

package fileio

func validatePlatformExactPath(_ string, _ string) error { return nil }
