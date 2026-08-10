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
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CheckURL — репозиторий консоли. var, а не const — тесты подменяют его на
// httptest.Server, не ходить же в GitHub ради проверки разбора ответа.
var CheckURL = "https://api.github.com/repos/ayanamisuicide/hrk-console/releases/latest"

// CleanVersionRe — "vX.Y.Z" без хвоста. У сборки из тега (git describe на
// чистом дереве без коммитов после тега) версия выглядит ровно так; у
// сборки из рабочего дерева (make gui/make build без тега, wails dev) — с
// суффиксом коммитов/-dirty или просто "dev". Сравнивать в обоих этих
// случаях не с чем: непонятно, новее хвостатая сборка последнего релиза
// или старее.
var CleanVersionRe = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// Release/Asset — часть ответа GitHub API "последний релиз", которая
// нужна: тег, страница релиза и ассеты (среди них ищем архив нужного
// бинарника).
type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name                string `json:"name"`
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

// downloadBinary скачивает архив релиза и достаёт из него единственный
// файл — сам бинарник: release.yml кладёт в архив только его, без вложенных
// путей (tar -czf ... -C bin hkc / -C build/bin hrk-console-gui), так что
// первый же обычный файл в архиве и есть искомое, искать по имени незачем.
// Возвращает путь к временному файлу с правом на исполнение — вызывающий
// обязан его удалить.
func downloadBinary(url string) (string, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("скачивание вернуло %d", resp.StatusCode)
	}

	gz, err := gzip.NewReader(resp.Body)
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
	rel, err := FetchLatest()
	if err != nil {
		return "", err
	}
	assetURL := FindAsset(rel, assetPrefix, assetSuffix)
	if assetURL == "" {
		return "", fmt.Errorf("в релизе нет подходящей сборки (%s*%s)", assetPrefix, assetSuffix)
	}

	tmpBinary, err := downloadBinary(assetURL)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpBinary)

	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	staged := exe + ".update"
	if err := copyFile(tmpBinary, staged); err != nil {
		return "", err
	}
	if err := os.Rename(staged, exe); err != nil {
		os.Remove(staged)
		return "", err
	}
	return rel.TagName, nil
}
