// Package botproc управляет процессом Heroku-юзербота: поиск по /proc,
// запуск в своей сессии, остановка, аптайм и версия из лога.
//
// /proc сканируется напрямую, а не через внешний pgrep: у Go-бинарника
// имя процесса не совпадает с шаблоном поиска, так что нет нужды в трюке
// с квадратными скобками ("[p]ython3"), которым bash-версия пряталась
// от собственного pgrep.
package botproc

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// needle ищет процесс "python3 -m heroku" по /proc/<pid>/cmdline.
const needle = "python3 -m heroku"

// Manager хранит каталог бота и путь к файлам, которые нужны для запуска.
type Manager struct {
	HerokuDir  string
	LogFile    string
	StartupLog string
	LockFile   string
}

func New(herokuDir string) *Manager {
	return &Manager{
		HerokuDir:  herokuDir,
		LogFile:    filepath.Join(herokuDir, "heroku.log"),
		StartupLog: filepath.Join(herokuDir, ".startup.log"),
		LockFile:   filepath.Join(herokuDir, ".launch.lock"),
	}
}

// PIDs возвращает все pid процессов бота (обычно один, но не гарантировано).
func PIDs() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue // процесс исчез между ReadDir и чтением — не бот
		}
		if bytes.Contains(bytes.ReplaceAll(data, []byte{0}, []byte{' '}), []byte(needle)) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// PID — первый найденный pid бота, 0 если не запущен.
func PID() int {
	pids := PIDs()
	if len(pids) == 0 {
		return 0
	}
	return pids[0]
}

func Alive() bool { return PID() != 0 }

// Uptime форматирует время жизни процесса: "1ч 12м" / "34м 05с" / "—".
func Uptime(pid int) string {
	if pid == 0 {
		return "—"
	}
	start, err := startTime(pid)
	if err != nil {
		return "—"
	}
	d := time.Since(start)
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dч %02dм", h, m)
	}
	return fmt.Sprintf("%dм %02dс", m, s)
}

// startTime вычисляет момент запуска процесса из /proc/<pid>/stat (поле 22,
// starttime в тиках с момента загрузки системы) и /proc/uptime.
func startTime(pid int) (time.Time, error) {
	statData, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return time.Time{}, err
	}
	// Имя процесса в скобках может содержать пробелы и закрывающие скобки —
	// поля начинаются после последней ")".
	i := bytes.LastIndexByte(statData, ')')
	if i < 0 {
		return time.Time{}, fmt.Errorf("некорректный /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(statData[i+2:]))
	const starttimeField = 22 - 3 // поля нумеруются с 1, первые два уже отрезаны
	if len(fields) <= starttimeField {
		return time.Time{}, fmt.Errorf("мало полей в /proc/%d/stat", pid)
	}
	ticks, err := strconv.ParseInt(fields[starttimeField], 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	clkTck := int64(100) // SC_CLK_TCK на Linux почти всегда 100
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return time.Time{}, err
	}
	uptimeFields := strings.Fields(string(uptimeData))
	sysUptime, err := strconv.ParseFloat(uptimeFields[0], 64)
	if err != nil {
		return time.Time{}, err
	}
	procUptimeSec := sysUptime - float64(ticks)/float64(clkTck)
	return time.Now().Add(-time.Duration(procUptimeSec * float64(time.Second))), nil
}

var versionRe = regexp.MustCompile(`Heroku (\d+\.\d+\.\d+)`)

// Version достаёт номер версии из последнего баннера старта в heroku.log.
// Читает хвост файла, а не весь файл: heroku.log растёт до 10 МБ, а нужна
// только последняя сессия.
func (m *Manager) Version() string {
	f, err := os.Open(m.LogFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tail = 256 * 1024
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	start := int64(0)
	if info.Size() > tail {
		start = info.Size() - tail
	}
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil {
		return ""
	}
	matches := versionRe.FindAllSubmatch(buf, -1)
	if len(matches) == 0 {
		return ""
	}
	return string(matches[len(matches)-1][1])
}

// Stop останавливает бота: сперва SIGTERM и до 5 секунд ожидания, затем
// SIGKILL, если не помогло. Возврат: 0 — остановлен штатно, 2 — пришлось
// убивать, 1 — не был запущен.
func (m *Manager) Stop() int {
	pids := PIDs()
	if len(pids) == 0 {
		return 1
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for i := 0; i < 50; i++ {
		if !Alive() {
			return 0
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, pid := range PIDs() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	time.Sleep(500 * time.Millisecond)
	return 2
}

// StartResult — итог попытки запуска.
type StartResult struct {
	PID      int
	AlreadyStarting bool // лок занят — кто-то уже стартует прямо сейчас
	Err      error
}

// Start поднимает бота в своей сессии (аналог setsid), отвязанным от
// текущего терминала: если бот получит .restart из Telegram и сделает
// killpg по своей группе процессов, это не заденет консоль. flock на
// LockFile не даёт двум параллельным стартам поднять два процесса разом.
func (m *Manager) Start() StartResult {
	lock, err := os.OpenFile(m.LockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return StartResult{Err: err}
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return StartResult{AlreadyStarting: true}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	if _, err := os.Stat(filepath.Join(m.HerokuDir, "venv", "bin", "activate")); err != nil {
		return StartResult{Err: fmt.Errorf("venv не найден: %w", err)}
	}

	out, err := os.Create(m.StartupLog)
	if err != nil {
		return StartResult{Err: err}
	}
	defer out.Close()

	cmd := exec.Command("bash", "-c", "source venv/bin/activate && exec python3 -m heroku")
	cmd.Dir = m.HerokuDir
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return StartResult{Err: err}
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release() // не ждём завершения — процесс живёт своей жизнью

	time.Sleep(300 * time.Millisecond) // дать процессу зацепиться за свою группу
	return StartResult{PID: pid}
}
