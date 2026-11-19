//go:build !linux

package fetch

func chownToSudoUser(dir, file string) {}
