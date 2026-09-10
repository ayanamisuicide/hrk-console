package selfupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"heroku-console/internal/releaseauth"
)

func downloadSignedBinary(rel *Release, prefix, suffix string, on ProgressFunc) (string, error) {
	name := prefix + rel.TagName + suffix
	var archiveURL, signatureURL string
	for _, asset := range rel.Assets {
		if asset.Name == name {
			archiveURL = asset.BrowserDownloadURL
		}
		if asset.Name == name+".sig" {
			signatureURL = asset.BrowserDownloadURL
		}
	}
	if archiveURL == "" || signatureURL == "" {
		return "", fmt.Errorf("в релизе нет подписанной сборки %s; установка отменена", name)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(signatureURL)
	if err != nil {
		return "", err
	}
	sig, readErr := io.ReadAll(io.LimitReader(resp.Body, ed25519.SignatureSize+1))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || readErr != nil || len(sig) != ed25519.SignatureSize {
		return "", fmt.Errorf("не удалось получить подпись обновления")
	}
	resp, err = client.Get(archiveURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("скачивание вернуло %d", resp.StatusCode)
	}
	archive, err := os.CreateTemp("", "hrk-console-archive-*")
	if err != nil {
		return "", err
	}
	defer func() { archive.Close(); os.Remove(archive.Name()) }()
	hash := sha256.New()
	body := &countingReader{r: io.LimitReader(resp.Body, maxArchiveBytes+1), total: resp.ContentLength, on: on, lastAt: time.Now()}
	n, err := io.Copy(io.MultiWriter(archive, hash), body)
	if err != nil {
		return "", err
	}
	if n > maxArchiveBytes {
		return "", fmt.Errorf("архив обновления слишком большой")
	}
	if err := releaseauth.Verify(name, hash.Sum(nil), sig); err != nil {
		return "", err
	}
	on.emit(Progress{Stage: StageDownload, Done: true, Bytes: n, Total: resp.ContentLength})
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	expected := strings.TrimSuffix(prefix, "-")
	if strings.Contains(suffix, "-windows-") {
		expected += ".exe"
	}
	result, err := extractBinary(archive, expected)
	if err != nil {
		return "", err
	}
	on.emit(Progress{Stage: StageUnpack, Done: true})
	return result, nil
}
