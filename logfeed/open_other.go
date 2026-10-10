//go:build !windows

package logfeed

import "os"

// openLog открывает журнал стандартным способом на платформах без Windows-режима совместного доступа.
func openLog(path string) (*os.File, error) { return os.Open(path) }
