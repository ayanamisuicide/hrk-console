//go:build !linux

package main

// На других системах сведения о процессах, сети и WSL не собираются: панель рассчитана на Linux/WSL.
type procScanner struct{}

func (*procScanner) scan() []procSample { return nil }

func openFiles(int) int { return -1 }

func readNetDev() (rx, tx uint64) { return 0, 0 }

func diskInodes(string) (total, free uint64) { return 0, 0 }

func detectWSL() wslInfo { return wslInfo{} }
