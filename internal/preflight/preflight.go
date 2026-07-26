// Package preflight — проверки, которые прогоняются перед запуском консоли.
//
// Две группы. Первая — окружение: есть ли каталог бота, читается ли лог,
// на месте ли venv. Эти проверки идут всегда, они дешёвые и отвечают на
// вопрос "почему ничего не работает" раньше, чем пользователь упрётся в
// пустой экран. Вторая — модульные тесты (go test), они запускаются, только
// если рядом есть исходники и Go; у пользователя с одним лишь бинарником
// проверять нечего, и такой прогон честно помечается как пропущенный.
package preflight

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Status — исход одной проверки.
type Status int

const (
	Pending Status = iota
	Running
	Passed
	Failed
	Skipped
)

// Check — одна проверка: имя для показа и функция, возвращающая пояснение
// и исход. Пояснение печатается рядом с именем — и при успехе тоже, потому
// что "venv найден" полезнее молчаливой галочки.
type Check struct {
	Name   string
	Run    func() (detail string, status Status)
	Status Status
	Detail string
	Took   time.Duration
}

// All собирает список проверок для каталога бота.
func All(herokuDir, logFile string) []Check {
	return []Check{
		{
			Name: "каталог бота",
			Run: func() (string, Status) {
				info, err := os.Stat(herokuDir)
				if err != nil {
					return herokuDir + " не найден", Failed
				}
				if !info.IsDir() {
					return herokuDir + " не каталог", Failed
				}
				return herokuDir, Passed
			},
		},
		{
			Name: "файл лога",
			Run: func() (string, Status) {
				info, err := os.Stat(logFile)
				if err != nil {
					// Лог появится при первом запуске бота — это не отказ,
					// смотреть просто пока нечего.
					return "ещё нет, появится при старте бота", Skipped
				}
				return humanSize(info.Size()), Passed
			},
		},
		{
			Name: "виртуальное окружение",
			Run: func() (string, Status) {
				p := filepath.Join(herokuDir, "venv", "bin", "activate")
				if _, err := os.Stat(p); err != nil {
					return "venv/bin/activate не найден — бот не запустится", Failed
				}
				return "venv/bin/activate", Passed
			},
		},
		{
			Name: "python3",
			Run: func() (string, Status) {
				p, err := exec.LookPath("python3")
				if err != nil {
					return "не найден в PATH", Failed
				}
				return p, Passed
			},
		},
		{
			Name: "tail",
			Run: func() (string, Status) {
				// tail -F держит слежение за файлом при ротации; своей
				// реализации слежения у нас нет, так что это жёсткая
				// зависимость, а не удобство.
				p, err := exec.LookPath("tail")
				if err != nil {
					return "не найден в PATH — живой просмотр не заработает", Failed
				}
				return p, Passed
			},
		},
		{
			Name: "права на запись",
			Run: func() (string, Status) {
				p := filepath.Join(herokuDir, ".launch.lock")
				f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY, 0o644)
				if err != nil {
					return "не могу создать .launch.lock", Failed
				}
				_ = f.Close()
				return ".launch.lock доступен", Passed
			},
		},
		{
			Name: "модульные тесты",
			Run:  goTest,
		},
	}
}

// goTest прогоняет "go test ./..." — только если рядом лежат исходники и
// установлен Go. Иначе честно сообщает, что проверять нечего: у бинарника,
// скачанного отдельно от репозитория, тестов рядом нет.
func goTest() (string, Status) {
	exe, err := os.Executable()
	if err != nil {
		return "не нашёл себя на диске", Skipped
	}
	// bin/hkc -> корень репозитория на уровень выше
	root := filepath.Dir(filepath.Dir(exe))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "исходники рядом не лежат", Skipped
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		// частая установка не в PATH — пробуем известное место
		alt := filepath.Join(os.Getenv("HOME"), ".local", "go", "bin", "go")
		if _, statErr := os.Stat(alt); statErr != nil {
			return "go не установлен", Skipped
		}
		goBin = alt
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "test", "./...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		return firstFailure(string(out)), Failed
	}
	return countOK(string(out)), Passed
}

// firstFailure вытаскивает из вывода go test первую значимую строку —
// показывать весь простыню в интерфейсе некуда.
func firstFailure(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "--- FAIL") || strings.HasPrefix(line, "FAIL") {
			return line
		}
	}
	return "тесты не прошли"
}

func countOK(out string) string {
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ok ") {
			n++
		}
	}
	if n == 0 {
		return "пройдены"
	}
	return "пройдены, пакетов: " + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return itoa(int(n>>20)) + " МБ"
	case n >= 1<<10:
		return itoa(int(n>>10)) + " КБ"
	default:
		return itoa(int(n)) + " Б"
	}
}
