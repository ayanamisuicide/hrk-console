package logfeed

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// collect ждёт n строк из потока или сдаётся по таймауту. Слежение
// опрашивает файл раз в четверть секунды, поэтому запас нужен щедрый.
func collect(t *testing.T, f *Follower, n int) []string {
	t.Helper()
	var out []string
	deadline := time.After(5 * time.Second)
	for len(out) < n {
		select {
		case line, ok := <-f.Lines:
			if !ok {
				t.Fatalf("поток закрылся, получено %d из %d строк: %q", len(out), n, out)
			}
			out = append(out, line)
		case <-deadline:
			t.Fatalf("не дождались строк: получено %d из %d (%q)", len(out), n, out)
		}
	}
	return out
}

func write(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestFollowerNewLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heroku.log")
	write(t, path, "старая строка\n")

	f, err := Follow(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	write(t, path, "первая\nвторая\n")
	got := collect(t, f, 2)
	if got[0] != "первая" || got[1] != "вторая" {
		t.Errorf("got %q", got)
	}
}

// from>0 — начать с указанной строки: так режим «перезапустить» показывает
// лог с момента старта бота, не проигрывая всю историю заново.
func TestFollowerFromLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heroku.log")
	write(t, path, "один\nдва\nтри\n")

	f, err := Follow(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	if got := collect(t, f, 2); got[0] != "два" || got[1] != "три" {
		t.Errorf("got %q, want [два три]", got)
	}
}

// Половина строки, застигнутая в момент записи, не должна уехать в поток
// как самостоятельная запись — она должна дождаться своего хвоста.
func TestFollowerWaitsForCompleteLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heroku.log")
	write(t, path, "")

	f, err := Follow(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	write(t, path, "начало строки")
	select {
	case line := <-f.Lines:
		t.Fatalf("отдали неполную строку: %q", line)
	case <-time.After(600 * time.Millisecond):
	}

	write(t, path, " и её конец\n")
	if got := collect(t, f, 1); got[0] != "начало строки и её конец" {
		t.Errorf("got %q", got[0])
	}
}

// Ротация: heroku/log.py крутит heroku.log на 10 МБ, подменяя файл. Новый
// файл читается с начала — иначе первые строки после ротации терялись бы.
func TestFollowerSurvivesRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heroku.log")
	write(t, path, "")

	f, err := Follow(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	write(t, path, "до ротации\n")
	collect(t, f, 1)

	if err := os.Rename(path, filepath.Join(dir, "heroku.log.1")); err != nil {
		t.Fatal(err)
	}
	write(t, path, "после ротации\n")

	if got := collect(t, f, 1); got[0] != "после ротации" {
		t.Errorf("got %q", got[0])
	}
}

// Обрезание файла на месте (truncate) — тоже подмена содержимого, читать
// надо с начала, а не с прежнего смещения.
func TestFollowerSurvivesTruncate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heroku.log")
	write(t, path, "длинная-длинная старая строка\n")

	f, err := Follow(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	write(t, path, "ещё строка\n")
	collect(t, f, 1)

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	write(t, path, "новая жизнь\n")

	if got := collect(t, f, 1); got[0] != "новая жизнь" {
		t.Errorf("got %q", got[0])
	}
}

// Пропавший файл — не повод молча замереть: состояние должно быть видно
// снаружи, а слежение обязано подхватить файл, когда он вернётся.
func TestFollowerReportsMissingFileAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heroku.log")

	f, err := Follow(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if ok, reason, _ := f.Alive(); !ok && reason != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("пропажа файла не отражена в состоянии")
		}
		time.Sleep(50 * time.Millisecond)
	}

	write(t, path, "файл вернулся\n")
	if got := collect(t, f, 1); got[0] != "файл вернулся" {
		t.Errorf("got %q", got[0])
	}
	if ok, _, _ := f.Alive(); !ok {
		t.Error("после возвращения файла слежение должно считаться живым")
	}
}

func TestFollowerStopClosesChannel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heroku.log")
	write(t, path, "")

	f, err := Follow(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Stop()
	f.Stop() // повторная остановка не должна паниковать

	select {
	case _, ok := <-f.Lines:
		if ok {
			t.Error("после остановки строк быть не должно")
		}
	case <-time.After(2 * time.Second):
		t.Error("канал не закрылся после остановки")
	}
}

func TestTailLinesAndCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heroku.log")
	write(t, path, "1\n2\n3\n4\n5\n")

	if got := LineCount(path); got != 5 {
		t.Errorf("LineCount: got %d, want 5", got)
	}
	got := TailLines(path, 2)
	if len(got) != 2 || got[0] != "4" || got[1] != "5" {
		t.Errorf("TailLines: got %q, want [4 5]", got)
	}
	if got := TailLines(path, 100); len(got) != 5 {
		t.Errorf("TailLines больше файла: got %d строк, want 5", len(got))
	}
}
