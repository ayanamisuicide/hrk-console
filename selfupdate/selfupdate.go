// Package selfupdate — единая логика самообновления для hkc и GUI: обе
// витрины проверяют один и тот же репозиторий на GitHub и умеют скачать
// свежий бинарник и атомарно подменить им себя на диске. Раньше это умел
// только GUI (gui/app.go) — TUI обновлялся вручную, git pull && make
// install; сама логика запроса к GitHub и подмены исполняемого файла для
// обеих одинаковая, разнится только имя архива в релизе.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CheckURL — репозиторий консоли. var, а не const — тесты подменяют его на
// httptest.Server, не ходить же в GitHub ради проверки разбора ответа.
var CheckURL = "https://api.github.com/repos/ayanamisuicide/hrk-console/releases/latest"

// DevListURL — список релизов, откуда берём последнюю dev-сборку. У канала
// "dev" нет одного стабильного адреса вроде /releases/latest — тот отдаёт
// только опубликованные не-prerelease, а у dev-канала своего постоянного
// тега тоже нет: каждый пуш в ветку dev получает собственный неизменяемый
// тег dev-<sha> (см. .github/workflows/dev-release.yml) и публикуется как
// prerelease. Список — единственный способ найти самый свежий из них.
var DevListURL = "https://api.github.com/repos/ayanamisuicide/hrk-console/releases"

// CleanVersionRe — "vX.Y.Z" без хвоста. У сборки из тега (git describe на
// чистом дереве без коммитов после тега) версия выглядит ровно так; у
// сборки из рабочего дерева (make gui/make build без тега, wails dev) — с
// суффиксом коммитов/-dirty или просто "dev". Сравнивать в обоих этих
// случаях не с чем: непонятно, новее хвостатая сборка последнего релиза
// или старее.
var CleanVersionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// DevVersionRe — "dev-<sha>", формат тега и версии dev-канала (см.
// .github/workflows/dev-release.yml). Отдельно от CleanVersionRe: это НЕ
// безымянная "нечего сравнивать" сборка вроде просто "dev" или git describe
// с хвостом коммитов — у неё есть чёткая идентичность, и когда пользователь
// явно переключился на канал stable, сидя на такой сборке, честный ответ
// "доступнее свежее" (сам факт, что тег не vX.Y.Z, это и значит), а не
// молчание, которое CheckChannel/Check держат для случая, когда сравнивать
// действительно не с чем.
var DevVersionRe = regexp.MustCompile(`^dev-[0-9a-f]+$`)

// Release/Asset — часть ответа GitHub API "последний релиз", которая
// нужна: тег, страница релиза и ассеты (среди них ищем архив нужного
// бинарника).
type Release struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Assets     []Asset `json:"assets"`
	Prerelease bool    `json:"prerelease"`
	// PublishedAt — единственный признак, по которому две сборки из РАЗНЫХ
	// каналов вообще сравнимы. Внутри канала сравнивают версии (VersionLess
	// для stable, равенство тегов для dev), но между каналами это
	// невозможно в принципе: в "dev-f0b43bd" номера нет, есть хеш коммита,
	// а у хеша нет порядка. Сказать "что из этого вышло позже" может только
	// время публикации.
	PublishedAt time.Time `json:"published_at"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// FetchLatest — единственное место, которое реально ходит в GitHub API. И
// Check (просто узнать, есть ли новее), и Apply (скачать и подменить себя)
// идут через него — два независимых запроса ради одного и того же ответа
// были бы тем же дублированием, что раньше чинили в statusLoop GUI для
// /proc.
func FetchLatest() (*Release, error) {
	req, err := http.NewRequest(http.MethodGet, CheckURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub ответил %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// fetchLatestDev — самая свежая dev-сборка: первый (GitHub отдаёт список
// новыми вперёд) релиз с prerelease=true. dev-release.yml помечает так
// каждый свой выпуск, а release.yml (стабильные теги) — никогда, так что
// смешать их в одном списке нечем.
func fetchLatestDev() (*Release, error) {
	req, err := http.NewRequest(http.MethodGet, DevListURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub ответил %d", resp.StatusCode)
	}
	var rels []Release
	if err := json.NewDecoder(resp.Body).Decode(&rels); err != nil {
		return nil, err
	}
	for i := range rels {
		if rels[i].Prerelease {
			return &rels[i], nil
		}
	}
	return nil, fmt.Errorf("dev-сборок среди релизов не нашлось")
}

// FindAsset ищет в релизе архив по имени: prefix+...+suffix. У hkc и GUI в
// одном релизе разные архивы (hkc-* и hrk-console-gui-*), поэтому просто
// "первый ассет" не годится — вызывающий обязан назвать свой, иначе Apply
// тихо скачал бы не то.
func FindAsset(rel *Release, prefix, suffix string) string {
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, prefix) && strings.HasSuffix(a.Name, suffix) {
			return a.BrowserDownloadURL
		}
	}
	return ""
}

// VersionLess сравнивает "vX.Y.Z" по номерам, а не строками — иначе v1.10.0
// проиграл бы v1.9.0 при обычном посимвольном сравнении. Вызывающий обязан
// убедиться, что оба аргумента прошли CleanVersionRe.
func VersionLess(a, b string) bool {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		na, _ := strconv.Atoi(pa[i])
		nb, _ := strconv.Atoi(pb[i])
		if na != nb {
			return na < nb
		}
	}
	return false
}

// CheckResult — ответ на Check: не только "нашлась новее версия", но и
// "проверили — уже последняя", и "проверить не вышло". OK значит "запрос
// отработал", а не "версия новая" — Available разводит эти два вопроса,
// иначе вызывающему было бы не отличить "сети нет" от "и так последняя".
type CheckResult struct {
	OK        bool
	Available bool
	Current   string
	Latest    string
	URL       string
	Message   string
}

// Check сравнивает current с последним релизом на GitHub.
func Check(current string) CheckResult {
	if !CleanVersionRe.MatchString(current) {
		return CheckResult{Current: current, Message: "сборка не из релиза (dev/грязное дерево) — сравнивать не с чем"}
	}
	rel, err := FetchLatest()
	if err != nil {
		return CheckResult{Current: current, Message: err.Error()}
	}
	if !CleanVersionRe.MatchString(rel.TagName) {
		return CheckResult{Current: current, Message: "релиз на GitHub в неожиданном формате версии"}
	}
	if !VersionLess(current, rel.TagName) {
		return CheckResult{OK: true, Current: current, Latest: rel.TagName}
	}
	return CheckResult{OK: true, Available: true, Current: current, Latest: rel.TagName, URL: rel.HTMLURL}
}

// CheckChannel — то же, что Check, но с явным каналом обновлений.
// "dev"-канал сравнивает не по семверу (у dev-тегов его нет и быть не
// может — dev-<sha>, не vX.Y.Z), а простым неравенством строк: тот ли это
// тег, что уже вшит в текущий бинарник. Работает это ровно потому, что
// dev-release.yml вшивает в -X main.version то же значение, что кладёт в
// тег релиза, — собранный из коммита dev-aeaba90 бинарник и релиз с тегом
// dev-aeaba90 совпадают буквально, без всякого парсинга версий.
func CheckChannel(channel, current string) CheckResult {
	if channel == "dev" {
		rel, err := fetchLatestDev()
		if err != nil {
			return CheckResult{Current: current, Message: err.Error()}
		}
		if rel.TagName == current {
			return CheckResult{OK: true, Current: current, Latest: rel.TagName}
		}
		return CheckResult{OK: true, Available: true, Current: current, Latest: rel.TagName, URL: rel.HTMLURL}
	}

	// Канал stable. Полностью безымянную сборку (просто "dev", git describe
	// с хвостом коммитов) Check() честно отказывается сравнивать — это
	// поведение остаётся как есть, TUI и hkc update от него зависят
	// (см. TestCheckSilentOnDevBuild). Но сборка dev-канала — не такая:
	// у неё есть настоящий тег, и раз она не vX.Y.Z, то определённо не
	// совпадает ни с одним стабильным релизом — откат на stable должен
	// суметь это увидеть, а не молчать вместе с действительно безымянными
	// сборками.
	if !CleanVersionRe.MatchString(current) && !DevVersionRe.MatchString(current) {
		return Check(current)
	}
	rel, err := FetchLatest()
	if err != nil {
		return CheckResult{Current: current, Message: err.Error()}
	}
	if !CleanVersionRe.MatchString(rel.TagName) {
		return CheckResult{Current: current, Message: "релиз на GitHub в неожиданном формате версии"}
	}
	if CleanVersionRe.MatchString(current) && !VersionLess(current, rel.TagName) {
		return CheckResult{OK: true, Current: current, Latest: rel.TagName}
	}
	return CheckResult{OK: true, Available: true, Current: current, Latest: rel.TagName, URL: rel.HTMLURL}
}

// ─── опрос обоих каналов сразу ───────────────────────────────────────────

// ChannelState — что лежит в одном канале, без вердикта «новее/старее».
// Вердикт внутри канала даёт CheckChannel; здесь сознательно только факты
// (тег и когда он опубликован), потому что предъявлять их предстоит рядом,
// а между каналами сравнивать нечего (см. Release.PublishedAt). IsCurrent
// отвечает на единственный вопрос, ответ на который честен всегда: не на
// этой ли сборке пользователь сидит прямо сейчас.
type ChannelState struct {
	Channel     string    `json:"channel"` // "" — stable, "dev" — dev
	OK          bool      `json:"ok"`      // запрос отработал
	Tag         string    `json:"tag"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"publishedAt"`
	IsCurrent   bool      `json:"isCurrent"`
	Message     string    `json:"message"` // чем кончилось, если !OK
}

// CheckBoth опрашивает stable и dev разом и отдаёт оба состояния как есть.
//
// Два запроса идут параллельно: они независимы, а таймаут у каждого 10с —
// последовательно худший случай складывался бы в двадцать, и экран, который
// это показывает, столько ждать не должен. Осечка одного канала не гасит
// второй: у каждого свой OK и своё Message.
func CheckBoth(current string) (stable, dev ChannelState) {
	stable.Channel = ""
	dev.Channel = "dev"

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		rel, err := FetchLatest()
		if err != nil {
			stable.Message = err.Error()
			return
		}
		stable.OK = true
		stable.Tag, stable.URL, stable.PublishedAt = rel.TagName, rel.HTMLURL, rel.PublishedAt
		stable.IsCurrent = rel.TagName == current
	}()

	go func() {
		defer wg.Done()
		rel, err := fetchLatestDev()
		if err != nil {
			dev.Message = err.Error()
			return
		}
		dev.OK = true
		dev.Tag, dev.URL, dev.PublishedAt = rel.TagName, rel.HTMLURL, rel.PublishedAt
		dev.IsCurrent = rel.TagName == current
	}()

	wg.Wait()
	return stable, dev
}

// ─── отчёт о ходе обновления ─────────────────────────────────────────────

// Stage — шаг обновления. Не выдуманная последовательность «для красоты»:
// это ровно то, что applyRelease и так делает по порядку, просто до сих пор
// молча. Единственный шаг, у которого есть осмысленное «сколько», —
// StageDownload: у остальных нет ни объёма, ни длительности, которую стоило
// бы показывать процентами.
type Stage string

const (
	StageQuery    Stage = "query"    // спрашиваем GitHub про релиз
	StageFind     Stage = "find"     // ищем в релизе нужный архив
	StageDownload Stage = "download" // качаем архив
	StageUnpack   Stage = "unpack"   // gzip + tar, достаём бинарник
	StageSwap     Stage = "swap"     // подмена на диске (или откладывание на Windows)
	StageDone     Stage = "done"
)

// Progress — одно сообщение о ходе. Done отделяет «шаг начался» от «шаг
// закончился»: без этого получатель не мог бы отличить идущее скачивание от
// завершившегося, кроме как по совпадению Bytes и Total, которого может и
// не случиться (Total = 0, если сервер не прислал Content-Length).
type Progress struct {
	Stage Stage  `json:"stage"`
	Done  bool   `json:"done"`
	Bytes int64  `json:"bytes"` // только StageDownload
	Total int64  `json:"total"` // 0 — размер неизвестен
	Note  string `json:"note"`  // тег, имя архива, путь — что уместно шагу
}

// ProgressFunc вызывается синхронно из горутины, которая делает само
// обновление. Может быть nil — тогда всё работает ровно как раньше.
type ProgressFunc func(Progress)

func (f ProgressFunc) emit(p Progress) {
	if f != nil {
		f(p)
	}
}

// countingReader считает прочитанное и дёргает колбэк, но не чаще раза в
// progressTick. Без ограничения по времени колбэк звался бы на каждый Read
// (десятки тысяч раз на пятимегабайтном архиве), и на той стороне это
// превратилось бы в такой же поток событий в webview, каким когда-то был
// построчный вывод лога.
type countingReader struct {
	r      io.Reader
	total  int64
	n      int64
	on     ProgressFunc
	lastAt time.Time
}

const progressTick = 100 * time.Millisecond

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if now := time.Now(); now.Sub(c.lastAt) >= progressTick {
		c.lastAt = now
		c.on.emit(Progress{Stage: StageDownload, Bytes: c.n, Total: c.total})
	}
	return n, err
}

// downloadBinary скачивает архив релиза и достаёт из него единственный
// файл — сам бинарник: release.yml кладёт в архив только его, без вложенных
// путей (tar -czf ... -C bin hkc / -C build/bin hrk-console-gui), так что
// первый же обычный файл в архиве и есть искомое, искать по имени незачем.
// Возвращает путь к временному файлу с правом на исполнение — вызывающий
// обязан его удалить.
func downloadBinary(url string, onProgress ProgressFunc) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("скачивание вернуло %d", resp.StatusCode)
	}

	// Счётчик стоит ДО gzip: считать надо то, что реально едет по сети
	// (сжатый поток, чей объём и обещан в Content-Length), а не то, во что
	// оно разворачивается, — иначе «скачано» перевалило бы за «всего».
	body := io.Reader(resp.Body)
	if onProgress != nil {
		body = &countingReader{r: resp.Body, total: resp.ContentLength, on: onProgress, lastAt: time.Now()}
	}

	gz, err := gzip.NewReader(body)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	out, err := os.CreateTemp("", "hrk-console-update-*")
	if err != nil {
		return "", err
	}
	defer out.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			os.Remove(out.Name())
			return "", fmt.Errorf("в архиве не нашёлся бинарник")
		}
		if err != nil {
			os.Remove(out.Name())
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if _, err := io.Copy(out, tr); err != nil {
			os.Remove(out.Name())
			return "", err
		}
		break
	}
	// Скачивание и распаковка идут одним потоком: tar тянет из gzip, gzip —
	// из сети, и «скачано» перестаёт расти ровно тогда, когда распаковано
	// последнее. Поэтому оба шага закрываются здесь, после io.Copy, а не
	// поодиночке где-то выше — иначе «скачано» отрапортовало бы о готовности,
	// пока байты ещё едут.
	if c, ok := body.(*countingReader); ok {
		onProgress.emit(Progress{Stage: StageDownload, Done: true, Bytes: c.n, Total: c.total})
	} else {
		onProgress.emit(Progress{Stage: StageDownload, Done: true})
	}
	onProgress.emit(Progress{Stage: StageUnpack, Done: true, Note: filepath.Base(out.Name())})

	if err := out.Chmod(0o755); err != nil {
		os.Remove(out.Name())
		return "", err
	}
	return out.Name(), nil
}

// copyFile читает src целиком и пишет в dst с правом на исполнение — файлы
// обновления маленькие (один бинарник), читать потоково смысла нет.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

// Apply скачивает бинарник assetPrefix*assetSuffix из последнего релиза и
// атомически подменяет им себя на диске. Не вызывается сам по себе в фоне —
// только по явному действию пользователя: подмена собственного
// исполняемого файла необратима без переустановки, и молчаливый автозапуск
// для такого не подходит.
//
// Замена атомарна в пределах одной директории (той же ФС, что и сам exe) —
// os.Rename либо подменяет файл целиком, либо не трогает его вовсе, никакого
// промежуточного "наполовину записанного бинарника" быть не может. Уже
// запущенный процесс продолжает работать со старым (теперь отвязанным от
// пути) файлом, пока не перезапустится сам.
func Apply(assetPrefix, assetSuffix string) (appliedVersion string, err error) {
	return ApplyChannelProgress("", assetPrefix, assetSuffix, nil)
}

// ApplyChannel — то же, что Apply, но качает из dev-канала вместо
// стабильного, когда channel == "dev". Сама подмена на диске — applyRelease,
// общая для обоих каналов: релиз найден, дальше не важно, откуда он.
func ApplyChannel(channel, assetPrefix, assetSuffix string) (appliedVersion string, err error) {
	return ApplyChannelProgress(channel, assetPrefix, assetSuffix, nil)
}

// ApplyChannelProgress — то же самое, но с отчётом о ходе. Отдельная
// функция, а не новый аргумент у ApplyChannel, ровно потому, что отчёт
// нужен одному вызывающему из трёх: GUI рисует по нему окно обновления,
// а `hkc update` и TUI обходятся текстовой строкой в конце. Ломать ради
// этого их сигнатуры было бы платой за чужую нужду; onProgress == nil
// возвращает прежнее поведение до последнего вызова.
func ApplyChannelProgress(channel, assetPrefix, assetSuffix string, onProgress ProgressFunc) (appliedVersion string, err error) {
	onProgress.emit(Progress{Stage: StageQuery})
	var rel *Release
	if channel == "dev" {
		rel, err = fetchLatestDev()
	} else {
		rel, err = FetchLatest()
	}
	if err != nil {
		return "", err
	}
	onProgress.emit(Progress{Stage: StageQuery, Done: true, Note: rel.TagName})
	return applyRelease(rel, assetPrefix, assetSuffix, onProgress)
}

// applyRelease скачивает assetPrefix*assetSuffix из уже найденного релиза
// (неважно, стабильного или dev — Apply/ApplyChannel сами разбираются,
// откуда его брать) и атомически подменяет им себя на диске.
//
// Замена атомарна в пределах одной директории (той же ФС, что и сам exe) —
// os.Rename либо подменяет файл целиком, либо не трогает его вовсе, никакого
// промежуточного "наполовину записанного бинарника" быть не может. Уже
// запущенный процесс продолжает работать со старым (теперь отвязанным от
// пути) файлом, пока не перезапустится сам.
func applyRelease(rel *Release, assetPrefix, assetSuffix string, onProgress ProgressFunc) (appliedVersion string, err error) {
	onProgress.emit(Progress{Stage: StageFind})
	assetURL := FindAsset(rel, assetPrefix, assetSuffix)
	if assetURL == "" {
		return "", fmt.Errorf("в релизе нет подходящей сборки (%s*%s)", assetPrefix, assetSuffix)
	}
	onProgress.emit(Progress{Stage: StageFind, Done: true, Note: path.Base(assetURL)})

	onProgress.emit(Progress{Stage: StageDownload})
	tmpBinary, err := downloadBinary(assetURL, onProgress)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpBinary)

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}

	onProgress.emit(Progress{Stage: StageSwap})
	if runtime.GOOS == "windows" {
		// Windows не даёт перезаписать (даже переименовать поверх) файл,
		// который прямо сейчас выполняется как этот процесс, — файл образа
		// заблокирован загрузчиком. Оставляем новую версию рядом
		// (exe+".new") — подмену и перезапуск делает вызывающая сторона уже
		// после того, как этот процесс завершится (см. PendingUpdate/
		// gui/app.go RestartApp): на Linux то же самое допустимо прямо
		// сейчас, os.Rename поверх открытого файла там безопасен и атомарен.
		if err := copyFile(tmpBinary, StagedPath(exe)); err != nil {
			return "", err
		}
		// Note отличает отложенную подмену от состоявшейся: на Windows файл
		// ляжет на место только после выхода процесса, и окну есть смысл
		// сказать об этом прямо, а не рапортовать "подменено".
		onProgress.emit(Progress{Stage: StageSwap, Done: true, Note: "отложено до перезапуска"})
		onProgress.emit(Progress{Stage: StageDone, Done: true, Note: rel.TagName})
		return rel.TagName, nil
	}

	staged := exe + ".update"
	if err := copyFile(tmpBinary, staged); err != nil {
		return "", err
	}
	if err := os.Rename(staged, exe); err != nil {
		os.Remove(staged)
		return "", err
	}
	onProgress.emit(Progress{Stage: StageSwap, Done: true, Note: exe})
	onProgress.emit(Progress{Stage: StageDone, Done: true, Note: rel.TagName})
	return rel.TagName, nil
}

// StagedPath — куда Apply на Windows кладёт скачанную версию, ожидающую
// подмены. Экспортирована, чтобы вызывающая сторона (RestartApp) могла
// проверить, есть ли обновление, ожидающее применения, не дублируя это имя
// у себя.
func StagedPath(exe string) string { return exe + ".new" }

// PendingUpdate возвращает путь к ожидающей подмене версии, если Apply на
// Windows её туда положил, и пусто, если ничего не ждёт.
func PendingUpdate(exe string) string {
	if _, err := os.Stat(StagedPath(exe)); err != nil {
		return ""
	}
	return StagedPath(exe)
}
