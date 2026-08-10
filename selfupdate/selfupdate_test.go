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

	path, err := downloadBinary(srv.URL)
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
	if _, err := downloadBinary(srv.URL); err == nil {
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
