package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"v1.7.6", "v1.7.7", true},
		{"v1.7.7", "v1.7.6", false},
		{"v1.7.6", "v1.7.6", false},
		{"v1.9.0", "v1.10.0", true},
		{"v2.0.0", "v1.99.99", false},
	}
	for _, c := range cases {
		if got := VersionLess(c.a, c.b); got != c.want {
			t.Errorf("VersionLess(%q, %q) = %v, ожидалось %v", c.a, c.b, got, c.want)
		}
	}
}

// CleanVersionRe отсеивает сборки из рабочего дерева (суффикс git describe,
// "dev") — для них сравнение версий ненадёжно, проверка обновлений должна
// промолчать, а не соврать "обновление доступно" на каждой dev-сборке.
func TestCleanVersionRe(t *testing.T) {
	for _, v := range []string{"v1.7.6", "v0.0.1"} {
		if !CleanVersionRe.MatchString(v) {
			t.Errorf("CleanVersionRe не совпал с чистым тегом %q", v)
		}
	}
	for _, v := range []string{"dev", "v1.7.6-3-gabc1234", "v1.7.6-dirty", "1.7.6"} {
		if CleanVersionRe.MatchString(v) {
			t.Errorf("CleanVersionRe ошибочно совпал с %q", v)
		}
	}
}

// FindAsset должен найти именно архив нужной витрины, а не первый попавшийся
// ассет релиза — рядом в том же релизе лежит архив другой (hkc-* и
// hrk-console-gui-* делят один релиз, с разным содержимым).
func TestFindAsset(t *testing.T) {
	rel := &Release{Assets: []Asset{
		{Name: "hkc-v1.8.1-linux-amd64.tar.gz", BrowserDownloadURL: "https://example/hkc"},
		{Name: "hrk-console-gui-v1.8.1-linux-amd64.tar.gz", BrowserDownloadURL: "https://example/gui"},
	}}
	if got := FindAsset(rel, "hrk-console-gui-", "-linux-amd64.tar.gz"); got != "https://example/gui" {
		t.Errorf("FindAsset вернул %q, ожидался gui-ассет", got)
	}
	if got := FindAsset(rel, "hkc-", "-linux-amd64.tar.gz"); got != "https://example/hkc" {
		t.Errorf("FindAsset вернул %q, ожидался hkc-ассет", got)
	}
	if got := FindAsset(&Release{}, "hkc-", "-linux-amd64.tar.gz"); got != "" {
		t.Errorf("FindAsset на пустом релизе вернул %q, ожидалась пустая строка", got)
	}
}

// downloadBinary разбирает настоящий tar.gz (не заглушку) — важно проверить
// именно распаковку, а не то, что HTTP-клиент умеет ходить по URL. Архив
// собирается in-memory, без реального обращения к GitHub.
func TestDownloadBinaryExtractsExecutable(t *testing.T) {
	want := []byte("fake-binary-content")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "hkc", Mode: 0o755, Size: int64(len(want))}); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if _, err := tw.Write(want); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	defer srv.Close()

	path, err := downloadBinary(srv.URL, nil)
	if err != nil {
		t.Fatalf("downloadBinary: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("чтение результата: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("содержимое %q, ожидалось %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Error("результат должен быть исполняемым")
	}
}

func TestDownloadBinaryFailsOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := downloadBinary(srv.URL, nil); err == nil {
		t.Error("ожидалась ошибка на HTTP 404")
	}
}

// Check молчит содержательно (OK=false с пояснением), а не паникует или
// врёт "обновление доступно" на dev-сборке — версия без чистого тега не с
// чем сравнивать.
func TestCheckSilentOnDevBuild(t *testing.T) {
	res := Check("dev")
	if res.OK {
		t.Errorf("Check(\"dev\") = %+v, ожидался OK=false — сравнивать не с чем", res)
	}
	if res.Message == "" {
		t.Error("Check на dev-сборке должен объяснить, почему не проверял")
	}
}

func TestCheckReportsAvailableAndUpToDate(t *testing.T) {
	origURL := CheckURL
	defer func() { CheckURL = origURL }()

	tag := "v1.0.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://example/release"}`))
	}))
	defer srv.Close()
	CheckURL = srv.URL

	tag = "v9.9.9"
	if res := Check("v1.0.0"); !res.OK || !res.Available || res.Latest != "v9.9.9" {
		t.Errorf("Check(v1.0.0) = %+v, ожидался available=true latest=v9.9.9", res)
	}

	tag = "v1.0.0"
	if res := Check("v1.0.0"); !res.OK || res.Available {
		t.Errorf("Check(v1.0.0) = %+v, ожидался available=false (уже последняя)", res)
	}
}

// Баг: переключение с dev-канала обратно на stable, сидя на честной
// dev-тег сборке ("vX.Y.Z-N-gHASH"), не должно молчать так же, как
// безымянная "dev"-сборка — иначе откат на stable невозможен вообще,
// кнопка "обновить" никогда не появляется.
func TestCheckChannelDevBuildSeesStableUpdate(t *testing.T) {
	origURL := CheckURL
	defer func() { CheckURL = origURL }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v1.13.0","html_url":"https://example/release"}`))
	}))
	defer srv.Close()
	CheckURL = srv.URL

	res := CheckChannel("", "v1.12.0-4-gaeaba90")
	if !res.OK || !res.Available || res.Latest != "v1.13.0" {
		t.Errorf("CheckChannel(\"\", \"v1.12.0-4-gaeaba90\") = %+v, ожидался available=true latest=v1.13.0", res)
	}
}

// Безымянная сборка (буквально "dev", без тега вовсе) на stable-канале
// по-прежнему молчит содержательно, а не врёт про доступное обновление —
// DevVersionRe не должен ловить то, что ловил CleanVersionRe как "нечего
// сравнивать".
func TestCheckChannelUnnamedDevBuildStillSilent(t *testing.T) {
	res := CheckChannel("", "dev")
	if res.OK {
		t.Errorf("CheckChannel(\"\", \"dev\") = %+v, ожидался OK=false — сравнивать не с чем", res)
	}
}

func TestCheckReportsNetworkError(t *testing.T) {
	origURL := CheckURL
	defer func() { CheckURL = origURL }()
	CheckURL = "http://127.0.0.1:1" // порт, на котором заведомо никто не слушает

	res := Check("v1.0.0")
	if res.OK {
		t.Errorf("Check() = %+v, ожидалась ошибка (OK=false)", res)
	}
	if res.Message == "" {
		t.Error("Check при сетевой ошибке должен объяснить, что пошло не так")
	}
}

// tarGzWith собирает настоящий tar.gz с одним файлом внутри — тот же приём,
// что и в TestDownloadBinaryExtractsExecutable, вынесенный ради тестов
// отчёта о ходе, которым нужен архив покрупнее.
func tarGzWith(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "hkc", Mode: 0o755, Size: int64(len(content))}); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("подготовка архива: %v", err)
	}
	return buf.Bytes()
}

// Отчёт о ходе обязан закрывать оба потоковых шага (скачивание и распаковка)
// и не закрывать скачивание раньше времени: tar тянет из gzip, а тот из
// сети, поэтому "скачано" перестаёт расти только после io.Copy. Регрессия,
// которую это ловит, — эмит StageDownload/Done сразу после gzip.NewReader,
// когда байты ещё едут.
func TestDownloadBinaryReportsProgress(t *testing.T) {
	archive := tarGzWith(t, bytes.Repeat([]byte("x"), 512*1024))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	var got []Progress
	path, err := downloadBinary(srv.URL, func(p Progress) { got = append(got, p) })
	if err != nil {
		t.Fatalf("downloadBinary: %v", err)
	}
	defer os.Remove(path)

	var downloadDone, unpackDone bool
	var maxBytes int64
	for _, p := range got {
		if p.Stage == StageDownload {
			if p.Bytes > maxBytes {
				maxBytes = p.Bytes
			}
			if p.Done {
				downloadDone = true
				if unpackDone {
					t.Error("скачивание закрылось после распаковки — порядок шагов перепутан")
				}
			}
		}
		if p.Stage == StageUnpack && p.Done {
			unpackDone = true
			if !downloadDone {
				t.Error("распаковка закрылась раньше скачивания")
			}
		}
	}
	if !downloadDone {
		t.Error("шаг скачивания не был закрыт")
	}
	if !unpackDone {
		t.Error("шаг распаковки не был закрыт")
	}
	if maxBytes != int64(len(archive)) {
		t.Errorf("насчитано %d байт, в архиве %d", maxBytes, len(archive))
	}
}

// nil-колбэк обязан оставлять прежнее поведение нетронутым: Apply/
// ApplyChannel зовут ApplyChannelProgress именно так, и TUI с `hkc update`
// зависят от того, что ничего не изменилось.
func TestDownloadBinaryWithoutProgressStillWorks(t *testing.T) {
	want := []byte("fake-binary-content")
	archive := tarGzWith(t, want)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	path, err := downloadBinary(srv.URL, nil)
	if err != nil {
		t.Fatalf("downloadBinary: %v", err)
	}
	defer os.Remove(path)
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("содержимое %q, ожидалось %q", got, want)
	}
}

// Запрос теперь один на оба канала, поэтому его осечка гасит оба — и оба
// обязаны честно назвать причину. Раньше запросов было два, и тест проверял
// их независимость; независимость ушла вместе со вторым запросом, ради
// вдвое меньшего расхода часового лимита GitHub.
func TestCheckBothReportsReasonInBothChannels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	oldCheck, oldList := CheckURL, DevListURL
	CheckURL, DevListURL = srv.URL, srv.URL
	defer func() { CheckURL, DevListURL = oldCheck, oldList }()

	stable, dev := CheckBoth("v1.15.0")

	if stable.OK || dev.OK {
		t.Error("оба канала должны быть !OK — запрос не удался")
	}
	if stable.Message == "" || dev.Message == "" {
		t.Errorf("причина потерялась: stable=%q dev=%q", stable.Message, dev.Message)
	}
}

// published_at обязан разбираться: без него каналы нечем сопоставить между
// собой, а весь экран обновлений построен именно на нём.
func TestCheckBothParsesPublishedAt(t *testing.T) {
	defer serveReleases(t, releasesInGitHubOrder)()
	oldCheck := CheckURL
	CheckURL = DevListURL
	defer func() { CheckURL = oldCheck }()

	stable, dev := CheckBoth("v1.0.0")
	if stable.PublishedAt.IsZero() || dev.PublishedAt.IsZero() {
		t.Errorf("published_at не разобран: stable=%v dev=%v", stable.PublishedAt, dev.PublishedAt)
	}
	if stable.Tag != "v1.15.0" || dev.Tag != "v1.14.0-9-g57dd2ab" {
		t.Errorf("выбраны не самые свежие: stable=%q dev=%q", stable.Tag, dev.Tag)
	}
}

// Порядок, в котором GitHub реально отдаёт /releases: сначала обычные
// релизы по убыванию даты, затем prerelease — отсортированные ПО ИМЕНИ
// ТЕГА, а не по времени. У dev-тегов имя — "vX.Y.Z-N-gHASH" от последнего
// стабильного релиза, при алфавитной сортировке строк это не лучше хеша:
// порядок в списке случаен. Здесь самый свежий dev (07:23) стоит последним,
// а первым — полуторачасовой давности: прежний код брал первый попавшийся
// prerelease и потому годами отдавал не ту сборку.
const releasesInGitHubOrder = `[
 {"tag_name":"v1.15.0","prerelease":false,"published_at":"2026-08-11T07:27:13Z"},
 {"tag_name":"v1.14.0","prerelease":false,"published_at":"2026-08-11T06:49:52Z"},
 {"tag_name":"v1.14.0-3-gf2e9642","prerelease":true,"published_at":"2026-08-11T06:00:10Z"},
 {"tag_name":"v1.14.0-6-gf0b43bd","prerelease":true,"published_at":"2026-08-11T06:45:40Z"},
 {"tag_name":"v1.14.0-1-g9626045","prerelease":true,"published_at":"2026-08-11T05:52:27Z"},
 {"tag_name":"v1.14.0-9-g57dd2ab","prerelease":true,"published_at":"2026-08-11T07:23:04Z"}
]`

func serveReleases(t *testing.T, body string) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	old := DevListURL
	DevListURL = srv.URL
	return func() { DevListURL = old; srv.Close() }
}

func TestFetchLatestDevPicksNewestNotFirst(t *testing.T) {
	defer serveReleases(t, releasesInGitHubOrder)()

	rel, err := fetchLatestDev()
	if err != nil {
		t.Fatalf("fetchLatestDev: %v", err)
	}
	if rel.TagName != "v1.14.0-9-g57dd2ab" {
		t.Errorf("выбрана сборка %q, а самая свежая по дате — v1.14.0-9-g57dd2ab", rel.TagName)
	}
}

// CheckBoth обязан выбирать по дате в обоих каналах и укладываться в ОДИН
// запрос: два запроса вдвое быстрее выбирали неаутентифицированный лимит
// GitHub (60/час на IP), после которого API отвечает 403 на всё подряд.
func TestCheckBothPicksNewestAndUsesOneRequest(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(releasesInGitHubOrder))
	}))
	defer srv.Close()
	oldCheck, oldList := CheckURL, DevListURL
	CheckURL, DevListURL = srv.URL, srv.URL
	defer func() { CheckURL, DevListURL = oldCheck, oldList }()

	stable, dev := CheckBoth("v1.15.0")

	if hits != 1 {
		t.Errorf("запросов к GitHub: %d, должен быть ровно один", hits)
	}
	if stable.Tag != "v1.15.0" {
		t.Errorf("stable.Tag = %q, ожидался v1.15.0", stable.Tag)
	}
	if !stable.IsCurrent {
		t.Error("stable.IsCurrent должен быть true — запущена ровно эта версия")
	}
	if dev.Tag != "v1.14.0-9-g57dd2ab" {
		t.Errorf("dev.Tag = %q, ожидался v1.14.0-9-g57dd2ab (самый свежий по дате)", dev.Tag)
	}
	if dev.IsCurrent {
		t.Error("dev.IsCurrent должен быть false — запущена стабильная сборка")
	}
}

// 403 от GitHub у неаутентифицированного клиента почти всегда значит
// исчерпанный часовой лимит. Голый код в сообщении не говорит ни причины,
// ни что делать, — заголовки говорят и то, и другое.
func TestHTTPErrorExplainsRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Limit", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(23*time.Minute).Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	old := DevListURL
	DevListURL = srv.URL
	defer func() { DevListURL = old }()

	_, err := fetchReleases()
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	msg := err.Error()
	if !strings.Contains(msg, "лимит") || !strings.Contains(msg, "60") {
		t.Errorf("сообщение %q не объясняет исчерпанный лимит", msg)
	}
	if strings.Contains(msg, "403") {
		t.Errorf("сообщение %q всё ещё сводится к голому коду ответа", msg)
	}
}
