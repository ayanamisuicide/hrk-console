package remotebot

import (
	"fmt"
	"strconv"
	"strings"

	"heroku-console/preflight"
	"heroku-console/setup"
)

// Preflight — те же восемь(без одной) проверок окружения, что и
// preflight.All локально (preflight/preflight.go), только через SSH: раз
// бот и всё его окружение реально живут на удалённой машине, проверять
// стоит именно её, а не то, чего (не) хватает машине, где просто открыто
// окно GUI.
//
// "модульные тесты" сюда не входит: та проверка гоняет go test по
// исходникам самой консоли (self-check hkc/hrk-console), не имеет
// отношения к тому, что стоит на машине с ботом, — гонять её там
// бессмысленно, и исходников hkc там нет и быть не должно.
func (c *Client) Preflight() []preflight.Check {
	dir := c.cfg.HerokuDir
	return []preflight.Check{
		{
			Name: "каталог бота",
			Run: func() (string, preflight.Status) {
				if _, err := c.run(fmt.Sprintf("test -d %s", shq(dir))); err != nil {
					return dir + " не найден или не каталог", preflight.Failed
				}
				return dir, preflight.Passed
			},
		},
		{
			Name: "файл лога",
			Run: func() (string, preflight.Status) {
				logFile := c.cfg.logFile()
				out, err := c.run(fmt.Sprintf("test -f %s && stat -c%%s %s", shq(logFile), shq(logFile)))
				if err != nil {
					// Лог появится при первом запуске бота — это не отказ,
					// смотреть просто пока нечего, тот же контракт, что и
					// у локальной версии.
					return "ещё нет, появится при старте бота", preflight.Skipped
				}
				size, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
				return preflight.HumanSize(size), preflight.Passed
			},
		},
		{
			Name: "виртуальное окружение",
			Run: func() (string, preflight.Status) {
				p := joinRemote(dir, "venv/bin/activate")
				if _, err := c.run(fmt.Sprintf("test -f %s", shq(p))); err != nil {
					return "venv/bin/activate не найден — бот не запустится", preflight.Failed
				}
				return "venv/bin/activate", preflight.Passed
			},
		},
		{
			Name: "python3",
			Run: func() (string, preflight.Status) {
				out, err := c.run("command -v python3")
				if err != nil {
					return "не найден в PATH", preflight.Failed
				}
				return strings.TrimSpace(string(out)), preflight.Passed
			},
		},
		{
			// Загрузчик Heroku молча пропускает модули с аудио и видео, если
			// ffmpeg нет: в чате модуль просто не появляется, причина видна
			// только в логе.
			Name: "ffmpeg",
			Run: func() (string, preflight.Status) {
				out, err := c.run("command -v ffmpeg")
				if err != nil {
					return "не найден — модули с аудио и видео не поставятся", preflight.Failed
				}
				return strings.TrimSpace(string(out)), preflight.Passed
			},
		},
		{
			// Зависимости загруженных модулей: requirements.txt бота про них
			// не знает, а ModuleNotFoundError виден только трейсбеком в логе.
			// Тот же DepScanScript, что и у локальной проверки (см. setup),
			// просто скормлен удалённому python'у через SSH, а не exec.Command.
			Name: "зависимости модулей",
			Run: func() (string, preflight.Status) {
				python := joinRemote(dir, "venv/bin/python")
				if _, err := c.run(fmt.Sprintf("test -f %s", shq(python))); err != nil {
					return "venv не найден — проверять нечем", preflight.Skipped
				}
				out, err := c.run(fmt.Sprintf("cd %s && venv/bin/python -c %s loaded_modules 2>&1",
					shq(dir), shq(setup.DepScanScript)))
				if err != nil {
					return "не удалось проверить: " + strings.TrimSpace(string(out)), preflight.Failed
				}
				if missing := setup.ParseDepOutput(string(out)); len(missing) > 0 {
					return "не хватает: " + strings.Join(missing, " "), preflight.Failed
				}
				return "импорты загруженных модулей резолвятся", preflight.Passed
			},
		},
		{
			Name: "права на запись",
			Run: func() (string, preflight.Status) {
				p := c.cfg.lockFile()
				if _, err := c.run(fmt.Sprintf("touch %s", shq(p))); err != nil {
					return "не могу создать .launch.lock", preflight.Failed
				}
				return ".launch.lock доступен", preflight.Passed
			},
		},
	}
}
