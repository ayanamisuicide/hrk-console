package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
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
// dev-тег сборке (dev-<sha>), не должно молчать так же, как безымянная
// "dev"-сборка — иначе откат на stable невозможен вообще, кнопка
// "обновить" никогда не появляется.
func TestCheckChannelDevBuildSeesStableUpdate(t *testing.T) {
	origURL := CheckURL
	defer func() { CheckURL = origURL }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"tag_name":"v1.13.0","html_url":"https://example/release"}`))
	}))
	defer srv.Close()
	CheckURL = srv.URL

	res := CheckChannel("", "dev-aeaba90")
	if !res.OK || !res.Available || res.Latest != "v1.13.0" {
		t.Errorf("CheckChannel(\"\", \"dev-aeaba90\") = %+v, ожидался available=true latest=v1.13.0", res)
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

// CheckBoth опрашивает каналы независимо: осечка одного не должна гасить
// второй — иначе недоступность одного адреса делала бы экран обновлений
// пустым вместо того, чтобы показать то, что всё-таки удалось узнать.
func TestCheckBothKeepsChannelsIndependent(t *testing.T) {
	stableSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v1.14.0","html_url":"https://example/stable","published_at":"2026-08-11T06:49:52Z"}`))
	}))
	defer stableSrv.Close()
	devSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer devSrv.Close()

	oldCheck, oldList := CheckURL, DevListURL
	CheckURL, DevListURL = stableSrv.URL, devSrv.URL
	defer func() { CheckURL, DevListURL = oldCheck, oldList }()

	stable, dev := CheckBoth("v1.14.0")

	if !stable.OK {
		t.Fatalf("stable должен был отработать, получено: %q", stable.Message)
	}
	if stable.Tag != "v1.14.0" {
		t.Errorf("stable.Tag = %q, ожидался v1.14.0", stable.Tag)
	}
	if !stable.IsCurrent {
		t.Error("stable.IsCurrent должен быть true — текущая версия совпадает с тегом")
	}
	if stable.PublishedAt.IsZero() {
		t.Error("published_at не разобран — без него каналы нечем сравнивать между собой")
	}
	if dev.OK {
		t.Error("dev не должен был отработать — сервер отвечает 500")
	}
	if dev.Message == "" {
		t.Error("dev.Message пуст — причина осечки потерялась")
	}
}
