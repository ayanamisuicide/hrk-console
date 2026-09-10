package main

import (
	"sync"
	"testing"
	"time"
)

func TestBackendSwitchWhilePolling(t *testing.T) {
	a := &App{backend: disconnectedBackend()}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			b := a.currentBackend()
			b.PID()
			b.AliveAt(1)
			b.Version()
		}
	}()
	for i := 0; i < 1000; i++ {
		a.setBackend(disconnectedBackend())
	}
	wg.Wait()
}

func TestDisconnectedBackendRejectsCommands(t *testing.T) {
	b := disconnectedBackend()
	if b.Start().Err == nil {
		t.Fatal("start succeeded without connection")
	}
	if _, err := b.Stop(); err == nil {
		t.Fatal("stop succeeded without connection")
	}
}

func TestWatchdogDoesNotRestartAcrossSSHOutage(t *testing.T) {
	h := newHarness(t)
	h.app.ui.Watchdog = true
	h.app.ui.Remote.Host = "test-host"
	h.app.remoteState = "offline"
	h.app.everAlive = true
	h.app.maybeWatchdogRestart(0)
	time.Sleep(20 * time.Millisecond)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.starts != 0 {
		t.Fatal("watchdog started bot while SSH was offline")
	}
}
