package remotebot

import (
	"os"
	"testing"

	"heroku-console/preflight"
)

// Имена и порядок проверок — то, на что фронтенд полагается напрямую
// (PreflightChecks рисует строки по этому списку раньше, чем придёт первое
// событие с результатом), поэтому сверяем их с preflight.All, а не с
// отдельным жёстко зашитым списком: переименование/добавление проверки в
// preflight.All, забытое здесь, иначе прошло бы мимо этого теста молча.
// "модульные тесты" исключены — Preflight() намеренно их не гоняет (см.
// комментарий у Preflight). Не требует живого соединения — сами имена не
// зависят от него.
func TestPreflightNames(t *testing.T) {
	c := &Client{cfg: Config{HerokuDir: "Heroku"}}
	checks := c.Preflight()

	var want []string
	for _, ch := range preflight.All("Heroku", "Heroku/heroku.log") {
		if ch.Name == "модульные тесты" {
			continue
		}
		want = append(want, ch.Name)
	}
	if len(checks) != len(want) {
		t.Fatalf("Preflight() вернул %d проверок, ожидалось %d: %v", len(checks), len(want), checks)
	}
	for i, name := range want {
		if checks[i].Name != name {
			t.Errorf("checks[%d].Name = %q, ожидалось %q", i, checks[i].Name, name)
		}
	}
}

// ─── интеграционный прогон против настоящей машины (опционален) ─────────
//
// Та же оговорка, что и у TestRemoteIntegration: не часть обычного
// `go test ./...`, в CI нет доступа к чужой домашней сети.
//
//	HKC_TEST_REMOTE_HOST=192.168.31.128 HKC_TEST_REMOTE_USER=ayanami \
//	HKC_TEST_REMOTE_KEY=~/.ssh/id_ed25519_hkc HKC_TEST_REMOTE_DIR=Heroku \
//	go test ./remotebot/... -run TestPreflightIntegration -v
func TestPreflightIntegration(t *testing.T) {
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

	for _, check := range c.Preflight() {
		detail, status := check.Run()
		t.Logf("%-24s %-8v %s", check.Name, status, detail)
		if status != preflight.Passed && status != preflight.Failed && status != preflight.Skipped {
			t.Errorf("%s: неожиданный статус %v", check.Name, status)
		}
	}
}
