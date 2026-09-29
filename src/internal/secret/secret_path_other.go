//go:build !windows

package secret

func secretPathIsReparsePoint(string) (bool, error) {
	return false, nil
}
