//go:build !linux

package botproc

import (
	"errors"
	"testing"
)

func TestStartUnsupported(t *testing.T) {
	res := New(t.TempDir()).Start()
	if !errors.Is(res.Err, errUnsupported) || res.PID != 0 {
		t.Fatalf("Start = %+v, expected unsupported platform", res)
	}
}
