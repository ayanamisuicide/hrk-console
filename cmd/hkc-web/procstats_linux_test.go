//go:build linux

package main

import (
	"os"
	"testing"
	"time"
)

// TestProcScannerSeesOwnProcess проверяет разбор /proc/PID/stat на собственном процессе теста:
// имя, память и потоки читаются, а повторный обход считает CPU без отрицательных значений.
func TestProcScannerSeesOwnProcess(t *testing.T) {
	var scanner procScanner
	scanner.scan()
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
	}
	samples := scanner.scan()
	for _, sample := range samples {
		if sample.CPUPercent < 0 {
			t.Fatalf("negative cpu: %+v", sample)
		}
		if sample.PID == os.Getpid() {
			if sample.RSS == 0 || sample.Threads == 0 || sample.Name == "" || sample.PPID != os.Getppid() {
				t.Fatalf("own process: %+v", sample)
			}
			if tree, ok := processTree(samples, sample.PID); !ok || tree.RSS < sample.RSS {
				t.Fatalf("tree: %+v", tree)
			}
			if openFiles(sample.PID) <= 0 {
				t.Fatal("open files")
			}
			return
		}
	}
	t.Fatal("own process not found")
}
