package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractRejectsDamagedArchives(t *testing.T) {
	valid := tarGzWith(t, []byte("binary"))
	badCRC := append([]byte(nil), valid...)
	badCRC[len(badCRC)-8] ^= 1
	for name, archive := range map[string][]byte{"crc": badCRC, "truncated": valid[:len(valid)-5], "not-gzip": []byte("invalid")} {
		t.Run(name, func(t *testing.T) {
			if path, err := extractBinary(bytes.NewReader(archive), "hkc"); err == nil {
				os.Remove(path)
				t.Fatal("damaged archive accepted")
			}
		})
	}
	if path, err := extractBinary(bytes.NewReader(valid), "hrk-console-gui.exe"); err == nil {
		os.Remove(path)
		t.Fatal("wrong executable accepted")
	}
}

func TestExtractRejectsUnexpectedEntries(t *testing.T) {
	for _, name := range []string{"../hkc", "dir/hkc", "dir\\hkc", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tw := tar.NewWriter(gz)
			entries := []string{name}
			if name == "duplicate" {
				entries = []string{"hkc", "other"}
			}
			for _, entry := range entries {
				if err := tw.WriteHeader(&tar.Header{Name: entry, Mode: 0o755, Size: 1}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			tw.Close()
			gz.Close()
			if path, err := extractBinary(&b, ""); err == nil {
				os.Remove(path)
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestSignedDownloadRejectsUnsignedAndForgedRelease(t *testing.T) {
	name := "hkc-v1.0.0-linux-amd64.tar.gz"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sig" {
			w.Write(make([]byte, 64))
			return
		}
		w.Write(tarGzWith(t, []byte("fake binary")))
	}))
	defer srv.Close()
	rel := &Release{TagName: "v1.0.0", Assets: []Asset{{Name: name, BrowserDownloadURL: srv.URL}}}
	if _, err := downloadSignedBinary(rel, "hkc-", "-linux-amd64.tar.gz", nil); err == nil {
		t.Fatal("unsigned release accepted")
	}
	rel.Assets = append(rel.Assets, Asset{Name: name + ".sig", BrowserDownloadURL: srv.URL + "/sig"})
	if _, err := downloadSignedBinary(rel, "hkc-", "-linux-amd64.tar.gz", nil); err == nil {
		t.Fatal("forged release accepted")
	}
}

func TestCopyFileKeepsDestinationOnFailure(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "app.new")
	if err := os.WriteFile(dst, []byte("previous"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(dir, dst); err == nil {
		t.Fatal("directory copy succeeded")
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "previous" {
		t.Fatalf("destination changed: %q, %v", b, err)
	}
	src := filepath.Join(dir, "download")
	if err := os.WriteFile(src, []byte("next"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(dst)
	if err != nil || string(b) != "next" {
		t.Fatalf("replacement failed: %q, %v", b, err)
	}
}

func TestSignedDownloadEndToEnd(t *testing.T) {
	name := "hkc-v0.0.0-linux-amd64.tar.gz"
	archive, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	signature, err := os.ReadFile(filepath.Join("testdata", name+".sig"))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sig" {
			w.Write(signature)
		} else {
			w.Write(archive)
		}
	}))
	defer srv.Close()
	rel := &Release{TagName: "v0.0.0", Assets: []Asset{
		{Name: name, BrowserDownloadURL: srv.URL},
		{Name: name + ".sig", BrowserDownloadURL: srv.URL + "/sig"},
	}}
	path, err := downloadSignedBinary(rel, "hkc-", "-linux-amd64.tar.gz", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "signed-test-binary" {
		t.Fatalf("download result: %q, %v", content, err)
	}
}
