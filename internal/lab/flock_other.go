//go:build !unix

package lab

import "os"

func flock(*os.File) error { return nil }
