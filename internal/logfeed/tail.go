package logfeed

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
)

// TailLines возвращает последние max сырых строк файла — используется для
// стартовой истории (--history N). Файл может быть до 10 МБ (heroku.log
// ротируется по этому порогу), поэтому хвост держим кольцевым буфером с
// индексацией по модулю, а не сдвигом массива на каждую строку — иначе
// на большом файле пересборка стала бы квадратичной.
func TailLines(path string, max int) []string {
	f, err := os.Open(path)
	if err != nil || max <= 0 {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	buf := make([]string, max)
	n := 0
	for scanner.Scan() {
		buf[n%max] = scanner.Text()
		n++
	}
	if n == 0 {
		return nil
	}
	if n < max {
		return buf[:n]
	}
	start := n % max
	out := make([]string, max)
	copy(out, buf[start:])
	copy(out[max-start:], buf[:start])
	return out
}

// LineCount считает строки файла — нужно, чтобы --from мог начать поток
// сразу после текущего конца лога, не показывая старую историю повторно.
func LineCount(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	n := 0
	for scanner.Scan() {
		n++
	}
	return n
}

// Follower читает новые строки файла лога через "tail -F" — не "-f":
// переживает ротацию (RotatingFileHandler в heroku/log.py крутит
// heroku.log на 10 МБ, "-f" на этом бы застрял, привязавшись к старому
// файловому дескриптору). Разумно опереться на давно отлаженную реализацию
// в coreutils, а не переизобретать слежение за инодом самим.
type Follower struct {
	Lines chan string
	Err   chan error
	cmd   *exec.Cmd
	cancel context.CancelFunc
}

// Follow запускает "tail -F" с заданного места. from<=0 — только новые
// строки (аналог "--from" не задан); from>0 — начиная с этой строки файла.
func Follow(path string, from int) (*Follower, error) {
	ctx, cancel := context.WithCancel(context.Background())
	args := []string{"-F"}
	if from > 0 {
		args = append(args, "-n", "+"+strconv.Itoa(from))
	} else {
		args = append(args, "-n", "0")
	}
	args = append(args, "--", path)

	cmd := exec.CommandContext(ctx, "tail", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	f := &Follower{
		Lines:  make(chan string, 256),
		Err:    make(chan error, 1),
		cmd:    cmd,
		cancel: cancel,
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			f.Lines <- scanner.Text()
		}
		close(f.Lines)
	}()

	return f, nil
}

func (f *Follower) Stop() {
	f.cancel()
	if f.cmd != nil && f.cmd.Process != nil {
		_ = f.cmd.Process.Kill()
	}
}
