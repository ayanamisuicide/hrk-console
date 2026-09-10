package main

import "fmt"

// botBackend is published as a complete instance, so status polling cannot see
// a mix of local and remote operations during asynchronous SSH startup.
type botBackend interface {
	PID() int
	AliveAt(int) bool
	Start() startResult
	Stop() (int, error)
	Uptime(int) string
	Version() string
}

type backendFuncs struct {
	pid        func() int
	aliveAt    func(int) bool
	start      func() startResult
	stop       func() (int, error)
	uptime     func(int) string
	botVersion func() string
}

func (b *backendFuncs) PID() int              { return b.pid() }
func (b *backendFuncs) AliveAt(pid int) bool  { return b.aliveAt(pid) }
func (b *backendFuncs) Start() startResult    { return b.start() }
func (b *backendFuncs) Stop() (int, error)    { return b.stop() }
func (b *backendFuncs) Uptime(pid int) string { return b.uptime(pid) }
func (b *backendFuncs) Version() string       { return b.botVersion() }

func (a *App) setBackend(b botBackend) {
	a.backendMu.Lock()
	a.backend = b
	a.backendMu.Unlock()
}

func (a *App) currentBackend() botBackend {
	a.backendMu.RLock()
	defer a.backendMu.RUnlock()
	return a.backend
}

func disconnectedBackend() botBackend {
	err := fmt.Errorf("нет SSH-подключения к боту")
	return &backendFuncs{
		pid: func() int { return 0 }, aliveAt: func(int) bool { return false },
		start:  func() startResult { return startResult{Err: err} },
		stop:   func() (int, error) { return 1, err },
		uptime: func(int) string { return "—" }, botVersion: func() string { return "" },
	}
}
