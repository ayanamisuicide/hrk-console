package state

import "testing"

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	want := State{ShowDebug: true, ShowSidebar: false, MinLevel: 2, Watchdog: true}
	Save(want)
	if got := Load(); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestLoadMissingReturnsDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if got := Load(); got != Default {
		t.Errorf("got %+v, want default %+v", got, Default)
	}
}
