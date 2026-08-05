package logfeed

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"
	"time"
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

// pollInterval — как часто проверяется, не дописали ли в файл. Логи бота
// идут пачками по несколько строк в секунду, и четверти секунды хватает,
// чтобы поток выглядел живым; более частый опрос будил бы процесс ради
// пустого stat.
const pollInterval = 250 * time.Millisecond

// Follower следит за файлом лога и отдаёт новые строки в канал.
//
// Раньше это был запущенный "tail -F": решение честное, но у него нет
// обратной связи. Если tail умирал (файл удалили и не пересоздали, процесс
// сняли), канал просто закрывался — вьюер оставался на экране с зелёной
// шапкой и переставал обновляться, и понять, почему лог замер, было
// нельзя. Плюс это была жёсткая внешняя зависимость, которую приходилось
// отдельно проверять перед запуском.
//
// Своя реализация повторяет то, ради чего брался именно "-F", а не "-f":
// переживает ротацию (RotatingFileHandler в heroku/log.py крутит
// heroku.log на 10 МБ) и обрезание файла, а состояние слежения при этом
// видно снаружи через Alive.
type Follower struct {
	Lines chan string

	stop     chan struct{}
	stopOnce sync.Once

	mu      sync.Mutex
	alive   bool      // файл открыт и читается
	lastErr string    // почему не читается, если не читается
	since   time.Time // с какого момента в этом состоянии
}

// Follow начинает слежение. from<=0 — только новые строки, from>0 —
// начиная с указанной строки файла (нумерация с единицы).
//
// Файл открывается синхронно, до возврата: позиция «с конца» иначе
// вычислялась бы в неизвестный момент после старта горутины, и всё, что бот
// успел записать между вызовом и первым чтением, молча терялось бы.
//
// Отсутствие файла ошибкой не считается — лог появится при первом запуске
// бота. Слежение подхватит его сам, а до тех пор состояние видно в Alive.
func Follow(path string, from int) (*Follower, error) {
	f := &Follower{
		Lines: make(chan string, 256),
		stop:  make(chan struct{}),
		since: time.Now(),
	}

	st := &followState{}
	if err := st.open(path, from); err != nil {
		f.setState(false, "файл лога недоступен")
	} else {
		f.setState(true, "")
	}

	go f.run(path, st)
	return f, nil
}

// followState — открытый файл и позиция чтения в нём.
type followState struct {
	file    *os.File
	reader  *bufio.Reader
	info    os.FileInfo
	offset  int64
	partial string
}

// open открывает файл и ставит чтение в нужную позицию. from>0 — с этой
// строки, from<=0 — с конца, from<0 — с начала (так открывается заново
// подменённый ротацией файл: его содержимое новое целиком, и пропускать
// в нём нечего).
func (s *followState) open(path string, from int) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	s.file = file
	s.info, _ = file.Stat()
	s.partial = ""
	s.offset = 0

	switch {
	case from > 0:
		s.offset = seekToLine(file, from)
	case from == 0:
		if end, err := file.Seek(0, io.SeekEnd); err == nil {
			s.offset = end
		}
	}
	s.reader = bufio.NewReaderSize(file, 64*1024)
	return nil
}

func (s *followState) close() {
	if s.file != nil {
		_ = s.file.Close()
		s.file, s.reader, s.info = nil, nil, nil
	}
}

// Alive сообщает, читается ли файл прямо сейчас, и если нет — почему и как
// давно. Вьюер показывает это в шапке: замерший лог должен объяснять себя
// сам, а не выглядеть как исправно работающий.
func (f *Follower) Alive() (ok bool, reason string, since time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive, f.lastErr, f.since
}

func (f *Follower) setState(alive bool, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.alive == alive && f.lastErr == reason {
		return // состояние не изменилось — время начала не сбрасываем
	}
	f.alive, f.lastErr, f.since = alive, reason, time.Now()
}

func (f *Follower) Stop() {
	f.stopOnce.Do(func() { close(f.stop) })
}

// run — цикл слежения. Дочитывает хвост, а при пропаже или подмене файла
// открывает его заново.
func (f *Follower) run(path string, s *followState) {
	defer close(f.Lines)
	defer s.close()

	for {
		select {
		case <-f.stop:
			return
		default:
		}

		if s.file == nil {
			// Заново открытый файл читается с начала: либо это новый файл
			// после ротации, либо тот же самый после обрезания — в обоих
			// случаях всё его содержимое мы ещё не видели.
			if err := s.open(path, -1); err != nil {
				f.setState(false, "файл лога недоступен")
				if !f.sleep(pollInterval) {
					return
				}
				continue
			}
			f.setState(true, "")
		}

		n, err := f.drain(s.reader, &s.offset, &s.partial)
		if err != nil {
			s.close()
			continue
		}
		if rotated(path, s.info, s.offset) {
			// Файл подменили или обрезали: дочитывать старый дескриптор
			// уже нечего — всё, что успели записать, мы прочитали выше.
			s.close()
			continue
		}
		if n == 0 && !f.sleep(pollInterval) {
			return
		}
	}
}

// drain вычитывает все целые строки, доступные прямо сейчас.
//
// Неполный хвост не отдаётся, а копится в partial: бот пишет строку не
// атомарно, и застать файл в момент записи — обычное дело. Отданная
// половина записи выглядела бы как оборванная строка лога, а следующая
// половина — как её продолжение с новым временем. Ровно этот баг чинился
// в bash-версии (0.2.0, "строки рвались пополам").
func (f *Follower) drain(r *bufio.Reader, offset *int64, partial *string) (int, error) {
	n := 0
	for {
		line, err := r.ReadString('\n')
		*offset += int64(len(line))
		if err != nil {
			*partial += line
			if err == io.EOF {
				return n, nil
			}
			return n, err
		}
		full := *partial + trimNewline(line)
		*partial = ""
		select {
		case f.Lines <- full:
		case <-f.stop:
			return n, io.EOF
		}
		n++
	}
}

// trimNewline снимает перевод строки вместе с CR: лог может приехать с
// файла, записанного не под Linux.
func trimNewline(s string) string {
	return strings.TrimSuffix(strings.TrimSuffix(s, "\n"), "\r")
}

// rotated отвечает, перестал ли открытый дескриптор быть тем файлом, за
// которым мы следим: имя указывает на другой инод (ротация) или файл стал
// короче прочитанного (обрезали на месте).
func rotated(path string, opened os.FileInfo, offset int64) bool {
	cur, err := os.Stat(path)
	if err != nil {
		return true
	}
	if opened != nil && !os.SameFile(opened, cur) {
		return true
	}
	return cur.Size() < offset
}

// sleep ждёт d или остановки. false — пора выходить.
func (f *Follower) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-f.stop:
		return false
	}
}

// seekToLine ставит чтение на начало строки number (нумерация с единицы) и
// возвращает смещение в байтах.
func seekToLine(file *os.File, number int) int64 {
	if number <= 1 {
		return 0
	}
	r := bufio.NewReaderSize(file, 64*1024)
	var offset int64
	for i := 1; i < number; i++ {
		line, err := r.ReadString('\n')
		offset += int64(len(line))
		if err != nil {
			break
		}
	}
	// Читатель набрал в буфер лишнего — возвращаем дескриптор ровно на
	// границу строки, чтобы дальше читать с неё.
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	return offset
}
