// Package preflight — проверки, которые прогоняются перед запуском консоли.
//
// Две группы. Первая — окружение: есть ли каталог бота, читается ли лог,
// на месте ли venv. Эти проверки идут всегда, они дешёвые и отвечают на
// вопрос "почему ничего не работает" раньше, чем пользователь упрётся в
// пустой экран. Вторая — модульные тесты (go test) по вшитому при сборке
// пути к исходникам; если репозитория на месте уже нет или Go не установлен,
// прогон честно помечается как пропущенный.
package preflight

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// repoRoot вшивается линкером при сборке (`-X ...preflight.repoRoot=`).
// Без него корень искался относительно бинарника — для `<репозиторий>/bin/hkc`
// это работало, а после `make install` бинарник живёт в ~/.local/bin, и
// проверка молча превращалась в "исходники рядом не лежат". То есть у
// установленной консоли тесты не запускались никогда.
var repoRoot string

// sourceRoot ищет каталог с go.mod: сначала вшитый при сборке путь, затем
// каталог над бинарником. Вшитый проверяется на существование — репозиторий
// могли перенести или удалить уже после сборки.
func sourceRoot() string {
	if repoRoot != "" && hasGoMod(repoRoot) {
		return repoRoot
	}
	if exe, err := os.Executable(); err == nil {
		if root := filepath.Dir(filepath.Dir(exe)); hasGoMod(root) {
			return root
		}
	}
	return ""
}

func hasGoMod(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

// goTest прогоняет тесты — только если исходники и Go на месте. Иначе честно
// сообщает, что проверять нечего.
//
// Запуск идёт с -count=1, то есть кэш результатов отключён: с кэшем go test
// возвращает "(cached)" за миллисекунды, и проверка сообщала бы "пройдены",
// ничего при этом не выполнив. Проверка, которая на самом деле ничего не
// проверяет, хуже отсутствующей.
func goTest() (string, Status) {
	root := sourceRoot()
	if root == "" {
		return "исходники не найдены", Skipped
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBin, "test", "-count=1", "-json", "./...")
	cmd.Dir = root
	out, _ := cmd.CombinedOutput()

	res := parseTestJSON(out)
	if res.failed > 0 {
		return fmt.Sprintf("упало %d из %d: %s", res.failed, res.passed+res.failed, res.firstFail), Failed
	}
	if ctx.Err() != nil {
		return "не уложились в 2 минуты", Failed
	}
	if res.passed == 0 {
		return "тесты не найдены", Skipped
	}
	return fmt.Sprintf("%s, пакетов: %d", plural(res.passed, "тест", "теста", "тестов"), res.packages), Passed
}

// testResult — итог разбора потока событий `go test -json`.
type testResult struct {
	passed    int
	failed    int
	packages  int
	firstFail string
}

// parseTestJSON считает результаты по событиям go test. Формат — одно
// событие на строку, поэтому разбираем построчно, а не потоковым Decoder'ом:
// на битой строке (паника рантайма или ошибка сборки печатаются мимо
// протокола) Decoder не сдвигает чтение за плохой токен, и пропуск ошибки
// превращался в вечный цикл на одном и том же месте.
func parseTestJSON(out []byte) testResult {
	var r testResult
	pkgs := make(map[string]bool)

	sc := bufio.NewScanner(bytes.NewReader(out))
	// Событие с Output может быть длинным — стандартных 64 КБ на строку
	// хватает не всегда, а обрыв разбора посреди прогона хуже лишней памяти.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue // строка мимо протокола
		}
		var ev struct {
			Action  string
			Package string
			Test    string
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		switch {
		case ev.Test == "" && ev.Action == "pass":
			pkgs[ev.Package] = true
		case ev.Test != "" && ev.Action == "pass":
			r.passed++
		case ev.Test != "" && ev.Action == "fail":
			r.failed++
			if r.firstFail == "" {
				r.firstFail = ev.Test
			}
		}
	}
	r.packages = len(pkgs)
	return r
}

// plural склоняет числительное: "1 тест", "2 теста", "5 тестов".
func plural(n int, one, few, many string) string {
	word := many
	switch {
	case n%10 == 1 && n%100 != 11:
		word = one
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20):
		word = few
	}
	return fmt.Sprintf("%d %s", n, word)
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
