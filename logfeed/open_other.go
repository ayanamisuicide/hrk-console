//go:build !windows

package logfeed

import "os"

func openLog(path string) (*os.File, error) { return os.Open(path) }
