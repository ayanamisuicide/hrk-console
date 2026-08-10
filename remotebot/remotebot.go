// Package remotebot — то же самое, что делают botproc и logfeed локально
// (найти процесс бота, запустить/остановить его, прочитать и отслеживать
// heroku.log), но когда бот живёт на другой машине, а GUI — на этой. На
// удалённой стороне ничего не ставится и не разворачивается: команды идут
// обычным SSH-exec (find-в-/proc, kill, tail), тем же самым способом,
// каким это делал бы человек в терминале. Это сознательный выбор, а не
// временное решение: разворачивать отдельный агент или собирать hkc под
// версию Go, которая может не совпадать с той, что стоит на удалённой
// машине, — лишняя движущаяся часть ради того, что и так делается тремя
// командами оболочки.
package remotebot

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// needle — тот же критерий, что и у botproc: подстрока в /proc/<pid>/cmdline.
const needle = "python3 -m heroku"

// Config — куда и как подключаться.
type Config struct {
	// Host — "192.168.31.128" или "192.168.31.128:2222"; порт по умолчанию 22.
	Host string
	User string
	// KeyPath — путь к приватному ключу (файл ищется как есть, без
	// раскрытия "~" — это дело вызывающего кода).
	KeyPath string
	// HerokuDir — каталог бота на удалённой машине, например "Heroku"
	// (относительно домашнего каталога пользователя) или абсолютный путь.
	HerokuDir string
	// KnownHostsPath — файл для TOFU: ключ незнакомого хоста запоминается
	// при первом подключении молча, а несовпадение уже запомненного —
	// нет, это уже похоже на подмену сервера, а не на первую встречу.
	KnownHostsPath string
}

func (c Config) logFile() string { return joinRemote(c.HerokuDir, "heroku.log") }
func (c Config) lockFile() string { return joinRemote(c.HerokuDir, ".launch.lock") }

// joinRemote — путь на удалённой стороне собирается для POSIX-шелла, а не
// через filepath (тот на Windows дал бы обратные слеши).
func joinRemote(dir, name string) string {
	if dir == "" {
		return name
	}
	return strings.TrimRight(dir, "/") + "/" + name
}

// Client — одно SSH-соединение и всё, что через него делается.
type Client struct {
	cfg  Config
	conn *ssh.Client
}

// Dial открывает соединение и сразу проверяет, что каталог бота вообще
// существует — лучше узнать об опечатке в пути сразу, а не на первой
// попытке что-то в нём сделать.
func Dial(cfg Config) (*Client, error) {
	key, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("чтение ключа: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("разбор ключа: %w", err)
	}
	hostKeyCB, err := tofuHostKeyCallback(cfg.KnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("known_hosts: %w", err)
	}

	addr := cfg.Host
	if !strings.Contains(addr, ":") {
		addr += ":22"
	}
	conn, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCB,
		Timeout:         8 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	c := &Client{cfg: cfg, conn: conn}

	if _, err := c.run(fmt.Sprintf("test -d %s", shq(cfg.HerokuDir))); err != nil {
		conn.Close()
		return nil, fmt.Errorf("каталог бота %q не найден на удалённой машине", cfg.HerokuDir)
	}
	return c, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// run выполняет одну команду и возвращает её stdout. Осечка несёт с собой
// stderr в тексте ошибки — "команда упала" само по себе бесполезно для
// диагностики, а "venv не найден: bash: venv/bin/activate: No such file"
// уже да.
func (c *Client) run(cmd string) ([]byte, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return nil, err
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if err := session.Run(cmd); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return stdout.Bytes(), fmt.Errorf("%w: %s", err, msg)
		}
		return stdout.Bytes(), err
	}
	return stdout.Bytes(), nil
}

// shq заключает строку в одинарные кавычки для безопасной подстановки в
// удалённую команду — путь к каталогу бота приходит из пользовательских
// настроек, а не константа, и должен обрабатываться так же осторожно, как
// любой ввод, попадающий в shell-команду.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// findPIDsScript — та же логика, что у botproc.PIDs(): подстрока
// "python3 -m heroku" в /proc/<pid>/cmdline, только выполняется удалённо
// обычным шеллом, а не Go-обходом директории. Собирает все совпавшие pid,
// не только первый — Stop() обязан остановить их все, как и локальная
// версия.
const findPIDsScript = `
pids=""
for p in /proc/[0-9]*; do
  [ -r "$p/cmdline" ] || continue
  if tr '\0' ' ' < "$p/cmdline" 2>/dev/null | grep -qF '` + needle + `'; then
    pids="$pids ${p#/proc/}"
  fi
done
`

// PIDs — все pid бота на удалённой машине.
func (c *Client) PIDs() ([]int, error) {
	out, err := c.run(findPIDsScript + `echo "$pids"`)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if n, err := strconv.Atoi(f); err == nil {
			pids = append(pids, n)
		}
	}
	return pids, nil
}

// PID — первый найденный, 0 если бот не запущен. Та же семантика, что и
// у botproc.PID().
func (c *Client) PID() (int, error) {
	pids, err := c.PIDs()
	if err != nil || len(pids) == 0 {
		return 0, err
	}
	return pids[0], nil
}

func (c *Client) Alive() (bool, error) {
	pid, err := c.PID()
	return pid != 0, err
}

// Uptime — тот же расчёт, что у botproc.startTime (поле 22 из
// /proc/<pid>/stat минус /proc/uptime), просто над байтами, которые
// принесла одна SSH-команда, а не над локальными файлами. Разбор
// продублирован, а не вынесен в общую с botproc функцию: там он читает
// файлы напрямую через os.ReadFile, здесь — уже готовые байты с другого
// транспорта, и подгонять одно под другое было бы менее читаемо, чем
// тридцать строк, которые редко меняются.
func (c *Client) Uptime(pid int) (string, error) {
	out, err := c.run(fmt.Sprintf(
		`cat /proc/%d/stat 2>/dev/null; echo ===HKC===; cat /proc/uptime 2>/dev/null`, pid))
	if err != nil {
		return "—", err
	}
	parts := bytes.SplitN(out, []byte("===HKC===\n"), 2)
	if len(parts) != 2 {
		return "—", errors.New("не удалось прочитать /proc на удалённой машине")
	}
	d, err := parseUptime(parts[0], parts[1])
	if err != nil {
		return "—", err
	}
	return formatUptime(d), nil
}

func parseUptime(statData, uptimeData []byte) (time.Duration, error) {
	i := bytes.LastIndexByte(statData, ')')
	if i < 0 || i+2 > len(statData) {
		return 0, errors.New("некорректный stat")
	}
	fields := strings.Fields(string(statData[i+2:]))
	const starttimeField = 22 - 3
	if len(fields) <= starttimeField {
		return 0, errors.New("мало полей в stat")
	}
	ticks, err := strconv.ParseInt(fields[starttimeField], 10, 64)
	if err != nil {
		return 0, err
	}
	uptimeFields := strings.Fields(string(uptimeData))
	if len(uptimeFields) == 0 {
		return 0, errors.New("пустой uptime")
	}
	sysUptime, err := strconv.ParseFloat(uptimeFields[0], 64)
	if err != nil {
		return 0, err
	}
	const clkTck = 100.0
	procUptimeSec := sysUptime - float64(ticks)/clkTck
	if procUptimeSec < 0 {
		procUptimeSec = 0
	}
	return time.Duration(procUptimeSec * float64(time.Second)), nil
}

func formatUptime(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dч %02dм", h, m)
	}
	return fmt.Sprintf("%dм %02dс", m, s)
}

var versionRe = regexp.MustCompile(`Heroku (\d+\.\d+\.\d+)`)

// Version — та же логика, что у botproc.Manager.Version: хвост лога, а не
// файл целиком, последнее совпадение баннера.
func (c *Client) Version() (string, error) {
	out, err := c.run(fmt.Sprintf("tail -c 262144 %s 2>/dev/null", shq(c.cfg.logFile())))
	if err != nil {
		return "", err
	}
	matches := versionRe.FindAllSubmatch(out, -1)
	if len(matches) == 0 {
		return "", nil
	}
	return string(matches[len(matches)-1][1]), nil
}

// StartResult — тот же контракт, что у botproc.StartResult.
type StartResult struct {
	PID             int
	AlreadyStarting bool
	Err             error
}

// Start — flock (утилита, не Go-syscall — на этой стороне нет прямого
// доступа к файловым дескрипторам удалённой машины) не даёт двум окнам
// стартовать бота одновременно, setsid отвязывает процесс от SSH-сессии:
// без него бот умер бы вместе с закрытием соединения. Тот же процесс, что
// у botproc.Start(), просто произнесённый шеллом, а не exec.Command.
func (c *Client) Start() StartResult {
	script := fmt.Sprintf(`
cd %s || { echo ERR:каталог бота не найден; exit 0; }
exec 9>%s
if ! flock -n 9; then echo ALREADY; exit 0; fi
%s
set -- $pids
if [ -n "${1:-}" ]; then echo "PID:$1"; exit 0; fi
if [ ! -f venv/bin/activate ]; then echo "ERR:venv не найден"; exit 0; fi
setsid bash -c 'exec > .startup.log 2>&1; source venv/bin/activate && exec python3 -m heroku' < /dev/null &
newpid=$!
disown
sleep 0.3
echo "PID:$newpid"
`, shq(c.cfg.HerokuDir), shq(c.cfg.lockFile()), findPIDsScript)

	out, err := c.run(script)
	if err != nil {
		return StartResult{Err: err}
	}
	line := strings.TrimSpace(string(out))
	switch {
	case line == "ALREADY":
		return StartResult{AlreadyStarting: true}
	case strings.HasPrefix(line, "ERR:"):
		return StartResult{Err: errors.New(strings.TrimPrefix(line, "ERR:"))}
	case strings.HasPrefix(line, "PID:"):
		pid, err := strconv.Atoi(strings.TrimPrefix(line, "PID:"))
		if err != nil {
			return StartResult{Err: err}
		}
		return StartResult{PID: pid}
	default:
		return StartResult{Err: fmt.Errorf("неожиданный ответ: %q", line)}
	}
}

// Stop — SIGTERM всем найденным pid и до 5 секунд ожидания на удалённой
// стороне одним заходом (не 50 SSH-команд подряд), затем SIGKILL, если не
// помогло. Коды возврата те же, что у botproc.Manager.Stop: 0 — остановлен
// штатно, 1 — не был запущен, 2 — пришлось убивать.
func (c *Client) Stop() (int, error) {
	script := findPIDsScript + `
if [ -z "$pids" ]; then echo 1; exit 0; fi
for p in $pids; do kill -TERM "$p" 2>/dev/null; done
for i in $(seq 1 50); do
  alive=0
  for p in $pids; do
    [ -r "/proc/$p/cmdline" ] || continue
    tr '\0' ' ' < "/proc/$p/cmdline" 2>/dev/null | grep -qF '` + needle + `' && alive=1
  done
  [ "$alive" = 0 ] && { echo 0; exit 0; }
  sleep 0.1
done
for p in $pids; do kill -KILL "$p" 2>/dev/null; done
sleep 0.5
echo 2
`
	out, err := c.run(script)
	if err != nil {
		return 0, err
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("неожиданный ответ Stop: %q", out)
	}
	return code, nil
}

// TailLines — последние n строк лога, для истории при открытии окна.
func (c *Client) TailLines(n int) ([]string, error) {
	out, err := c.run(fmt.Sprintf("tail -n %d %s 2>/dev/null", n, shq(c.cfg.logFile())))
	if err != nil {
		return nil, err
	}
	return splitLines(out), nil
}

func splitLines(b []byte) []string {
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// FollowHandle — поток новых строк лога и способ его остановить. Здоровье
// потока (alive/lastErr/since) — тот же контракт, что у logfeed.Follower.Alive()
// локально: GUI показывает баннер "лог не читается", когда опрос начинает
// стабильно падать (сеть моргнула, файл на удалённой стороне пропал), и не
// должен знать, локальный это хвост или удалённый — только что он вообще
// умеет ответить на вопрос "жив?".
type FollowHandle struct {
	Lines chan string
	stop  chan struct{}

	mu      sync.Mutex
	alive   bool
	lastErr string
	since   time.Time
}

func (h *FollowHandle) Stop() { close(h.stop) }

func (h *FollowHandle) Alive() (ok bool, reason string, since time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.alive, h.lastErr, h.since
}

func (h *FollowHandle) setState(alive bool, reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.alive == alive && h.lastErr == reason {
		return
	}
	h.alive, h.lastErr, h.since = alive, reason, time.Now()
}

// followPollInterval — тот же порядок, что у logfeed.Follow локально
// (250мс): опрос файла, а не подписка на события, по тем же причинам —
// переживает ротацию и не требует ничего, кроме обычного чтения.
const followPollInterval = 250 * time.Millisecond

// Follow начинает слежение с текущего конца файла — ровно то, что нужно
// вызывающему (gui/app.go всегда хочет "не пропустить ничего нового с
// этого момента", исторические строки приходят отдельно через TailLines);
// произвольный номер строки как параметр был бы возможностью, которой
// никто не пользуется.
func (c *Client) Follow() (*FollowHandle, error) {
	size, err := c.fileSize()
	if err != nil {
		return nil, err
	}
	h := &FollowHandle{Lines: make(chan string, 256), stop: make(chan struct{})}
	h.setState(true, "")
	go c.followLoop(size, h)
	return h, nil
}

func (c *Client) fileSize() (int64, error) {
	out, err := c.run(fmt.Sprintf("stat -c%%s %s 2>/dev/null || echo 0", shq(c.cfg.logFile())))
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// tailFrom возвращает текущий размер файла и байты, появившиеся после
// offset, одной командой — раздельные "узнать размер" и "прочитать хвост"
// были бы лишним круговым обходом на каждый опрос.
func (c *Client) tailFrom(offset int64) (size int64, chunk []byte, err error) {
	out, err := c.run(fmt.Sprintf(
		`sz=$(stat -c%%s %s 2>/dev/null || echo 0); echo "$sz"; tail -c +%d %s 2>/dev/null`,
		shq(c.cfg.logFile()), offset+1, shq(c.cfg.logFile())))
	if err != nil {
		return 0, nil, err
	}
	i := bytes.IndexByte(out, '\n')
	if i < 0 {
		return 0, nil, errors.New("неожиданный ответ tailFrom")
	}
	size, err = strconv.ParseInt(string(out[:i]), 10, 64)
	if err != nil {
		return 0, nil, err
	}
	return size, out[i+1:], nil
}

func (c *Client) followLoop(startOffset int64, h *FollowHandle) {
	defer close(h.Lines)
	offset := startOffset
	var pending []byte
	ticker := time.NewTicker(followPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ticker.C:
		}
		size, chunk, err := c.tailFrom(offset)
		if err != nil {
			// Временная сетевая заминка — не повод останавливать слежение,
			// но повод сказать пользователю, что происходит: та же роль,
			// что у reason в logfeed.Follower.Alive() локально.
			h.setState(false, err.Error())
			continue
		}
		h.setState(true, "")
		if size < offset {
			// Файл обрезан или пересоздан (ротация) — начинаем с начала,
			// тот же приём, что у logfeed.Follow локально.
			offset = 0
			pending = nil
			continue
		}
		offset = size
		pending = append(pending, chunk...)
		for {
			i := bytes.IndexByte(pending, '\n')
			if i < 0 {
				break // недописанная строка ждёт следующего опроса
			}
			line := string(pending[:i])
			pending = pending[i+1:]
			select {
			case h.Lines <- line:
			case <-h.stop:
				return
			}
		}
	}
}

// ─── TOFU-проверка ключа хоста ──────────────────────────────────────────

// tofuHostKeyCallback — при первом подключении к хосту его ключ просто
// запоминается (Trust On First Use), при повторном — сверяется. Несовпадение
// уже запомненного ключа не принимается автоматически никогда: это ровно
// то, от чего защищает проверка ключа хоста (иначе первое же MITM тихо
// перезаписало бы себя как доверенного).
func tofuHostKeyCallback(path string) (ssh.HostKeyCallback, error) {
	if err := ensureFile(path); err != nil {
		return nil, err
	}
	verify, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := verify(hostname, remote, key)
		if err == nil {
			return nil
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			return appendKnownHost(path, hostname, key)
		}
		return err
	}, nil
}

func ensureFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func appendKnownHost(path, hostname string, key ssh.PublicKey) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key) + "\n"
	_, err = f.WriteString(line)
	return err
}
