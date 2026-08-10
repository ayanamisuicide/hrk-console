package remotebot

import (
	"os"
	"testing"
	"time"
)

func TestShqEscapesSingleQuotes(t *testing.T) {
	cases := map[string]string{
		"Heroku":       `'Heroku'`,
		"it's":         `'it'\''s'`,
		"":             `''`,
		"a'b'c":        `'a'\''b'\''c'`,
	}
	for in, want := range cases {
		if got := shq(in); got != want {
			t.Errorf("shq(%q) = %q, ожидалось %q", in, got, want)
		}
	}
}

func TestJoinRemoteUsesForwardSlash(t *testing.T) {
	cases := []struct{ dir, name, want string }{
		{"Heroku", "heroku.log", "Heroku/heroku.log"},
		{"Heroku/", "heroku.log", "Heroku/heroku.log"},
		{"/home/ayanami/Heroku", ".launch.lock", "/home/ayanami/Heroku/.launch.lock"},
	}
	for _, c := range cases {
		if got := joinRemote(c.dir, c.name); got != c.want {
			t.Errorf("joinRemote(%q, %q) = %q, ожидалось %q", c.dir, c.name, got, c.want)
		}
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"\n", nil},
		{"one line, no trailing newline", []string{"one line, no trailing newline"}},
		{"a\nb\nc\n", []string{"a", "b", "c"}},
		{"a\nb\nc", []string{"a", "b", "c"}},
	}
	for _, c := range cases {
		got := splitLines([]byte(c.in))
		if len(got) != len(c.want) {
			t.Fatalf("splitLines(%q) = %v, ожидалось %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitLines(%q)[%d] = %q, ожидалось %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// Вход тот же, что реально приезжает по SSH: "cat /proc/<pid>/stat" и
// "cat /proc/uptime", просто зафиксированный руками, а не снятый с живой
// системы — цифры должны сойтись, откуда бы они ни взялись.
func TestParseUptime(t *testing.T) {
	// starttime (поле 22, здесь третье после ")") = 500 тиков по 100/с = 5с
	// от старта системы; сама система жила 65с — значит процесс живёт 60с.
	stat := []byte("12345 (python3) S 1 12345 12345 0 -1 4194560 0 0 0 0 0 0 0 0 20 0 1 0 500 0 0\n")
	uptime := []byte("65.00 120.00\n")

	d, err := parseUptime(stat, uptime)
	if err != nil {
		t.Fatalf("parseUptime: %v", err)
	}
	if got := d.Round(time.Second); got != 60*time.Second {
		t.Errorf("parseUptime = %v, ожидалось 60с", got)
	}
}

func TestParseUptimeRejectsGarbage(t *testing.T) {
	if _, err := parseUptime([]byte("не /proc/stat вовсе"), []byte("1.0 1.0")); err == nil {
		t.Error("ожидалась ошибка на битом stat")
	}
}

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{45 * time.Second, "0м 45с"},
		{90 * time.Second, "1м 30с"},
		{75 * time.Minute, "1ч 15м"},
	}
	for _, c := range cases {
		if got := formatUptime(c.d); got != c.want {
			t.Errorf("formatUptime(%v) = %q, ожидалось %q", c.d, got, c.want)
		}
	}
}

// ─── интеграционный прогон против настоящей машины (опционален) ─────────
//
// Не часть обычного `go test ./...` — в CI нет доступа к чужой домашней
// сети, и гонять его там нечем. Запускается вручную, когда под рукой есть
// реальный хост:
//
//	HKC_TEST_REMOTE_HOST=192.168.31.128 HKC_TEST_REMOTE_USER=ayanami \
//	HKC_TEST_REMOTE_KEY=~/.ssh/id_ed25519_hkc HKC_TEST_REMOTE_DIR=Heroku \
//	go test ./remotebot/... -run TestRemoteIntegration -v
func TestRemoteIntegration(t *testing.T) {
	host := os.Getenv("HKC_TEST_REMOTE_HOST")
	if host == "" {
		t.Skip("HKC_TEST_REMOTE_HOST не задан — пропускаю (это не автоматический CI-тест)")
	}
	cfg := Config{
		Host:           host,
		User:           os.Getenv("HKC_TEST_REMOTE_USER"),
		KeyPath:        os.Getenv("HKC_TEST_REMOTE_KEY"),
		HerokuDir:      os.Getenv("HKC_TEST_REMOTE_DIR"),
		KnownHostsPath: os.Getenv("HKC_TEST_REMOTE_KEY") + "_known_hosts_test",
	}

	c, err := Dial(cfg)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	pid, err := c.PID()
	if err != nil {
		t.Fatalf("PID: %v", err)
	}
	t.Logf("PID: %d", pid)
	if pid == 0 {
		t.Skip("бот на удалённой машине не запущен — дальше проверять нечего")
	}

	uptime, err := c.Uptime(pid)
	if err != nil {
		t.Fatalf("Uptime: %v", err)
	}
	t.Logf("Uptime: %s", uptime)
	if uptime == "—" {
		t.Error("Uptime вернул прочерк для живого процесса")
	}

	version, err := c.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	t.Logf("Version: %q", version)

	lines, err := c.TailLines(5)
	if err != nil {
		t.Fatalf("TailLines: %v", err)
	}
	t.Logf("TailLines(5) вернул %d строк, последняя: %q", len(lines), lastOrEmpty(lines))
	if len(lines) == 0 {
		t.Error("TailLines(5) на непустом логе вернул 0 строк")
	}

	h, err := c.Follow()
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	select {
	case line, ok := <-h.Lines:
		if ok {
			t.Logf("Follow поймал новую строку: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Log("Follow: новых строк за 3с не было (бот просто молчал — не ошибка)")
	}
	h.Stop()
}

func lastOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}
