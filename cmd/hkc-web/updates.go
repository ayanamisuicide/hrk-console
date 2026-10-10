package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

const projectURL = "https://github.com/ayanamisuicide/hrk-console"

var buildVersion = "dev"
var buildCommit = ""
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// versionInfo — Версия работающего бинарника, встроенный коммит и признак сборки с локальными правками.
type versionInfo struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Modified bool   `json:"modified"`
}

// currentVersion объединяет встроенную версию сборки с доступными VCS-метаданными Go.
func currentVersion() versionInfo {
	v := versionInfo{Version: buildVersion, Commit: buildCommit}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && v.Commit == "" {
				v.Commit = setting.Value
			}
			if setting.Key == "vcs.modified" {
				v.Modified = setting.Value == "true"
			}
		}
	}
	return v
}

// sourceInfo — Результат проверки одной копии Git без изменения её файлов.
type sourceInfo struct {
	Configured bool   `json:"configured"`
	Commit     string `json:"commit,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Dirty      bool   `json:"dirty"`
	Error      string `json:"error,omitempty"`
}

// gitOutput запускает Git с контекстом, без пользовательских хуков и интерактивных запросов.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	options := []string{"-c", "core.hooksPath=/dev/null"}
	// Git в Windows использует CRLF; при проверке из WSL сохраняем ту же настройку.
	if strings.HasPrefix(dir, "/mnt/") {
		options = append(options, "-c", "core.autocrlf=true")
	}
	if dir != "" {
		options = append(options, "-C", dir)
	}
	options = append(options, args...)
	cmd := exec.CommandContext(ctx, "git", options...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(data)), nil
}

// inspectSource проверяет путь, ветку, коммит и локальные изменения копии исходников без её изменения.
func inspectSource(ctx context.Context, dir string) sourceInfo {
	if dir == "" {
		return sourceInfo{}
	}
	v := sourceInfo{Configured: true}
	var err error
	v.Commit, err = gitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Branch, err = gitOutput(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		v.Branch = "detached"
	}
	status, err := gitOutput(ctx, dir, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Dirty = status != ""
	return v
}

// remoteVersion — Стабильный тег релиза и подтверждённый коммит удалённого репозитория.
type remoteVersion struct {
	Version   string    `json:"version,omitempty"`
	Commit    string    `json:"commit,omitempty"`
	Main      string    `json:"main,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	Error     string    `json:"error,omitempty"`
	Checking  bool      `json:"checking"`
}

// updateChecker — Последний результат фоновой проверки релиза, защищённый от параллельных обращений.
type updateChecker struct {
	mu     sync.Mutex
	remote remoteVersion
}

// snapshot возвращает защищённую мьютексом копию результата проверки обновлений.
func (c *updateChecker) snapshot() remoteVersion {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remote
}

// check обновляет сведения о релизе в фоне, не допуская параллельных и слишком частых проверок.
func (c *updateChecker) check() {
	c.mu.Lock()
	if c.remote.Checking || time.Since(c.remote.CheckedAt) < time.Minute {
		c.mu.Unlock()
		return
	}
	c.remote.Checking = true
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		remote, err := fetchRemoteVersion(ctx)
		remote.CheckedAt = time.Now().UTC()
		if err != nil {
			remote.Error = err.Error()
		}
		c.mu.Lock()
		c.remote = remote
		c.mu.Unlock()
	}()
}

// fetchRemoteVersion получает стабильный тег через HTTPS-переадресацию релиза.
// Его коммит сверяется по ссылкам Git, а не по тексту названия релиза.
func fetchRemoteVersion(ctx context.Context) (remoteVersion, error) {
	// Публичная переадресация релиза не требует токена и не расходует лимит GitHub API.
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 || req.URL.Scheme != "https" || req.URL.Host != "github.com" {
			return fmt.Errorf("неожиданная переадресация релиза")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, projectURL+"/releases/latest", nil)
	if err != nil {
		return remoteVersion{}, err
	}
	response, err := client.Do(req)
	if err != nil {
		return remoteVersion{}, fmt.Errorf("GitHub недоступен: %w", err)
	}
	response.Body.Close()
	prefix := projectURL + "/releases/tag/"
	finalURL := response.Request.URL.String()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(finalURL, prefix) {
		return remoteVersion{}, fmt.Errorf("GitHub: стабильный релиз недоступен (HTTP %d)", response.StatusCode)
	}
	tag := strings.TrimPrefix(finalURL, prefix)
	if !releaseTag.MatchString(tag) {
		return remoteVersion{}, fmt.Errorf("неподдерживаемый тег релиза")
	}
	refs, err := gitOutput(ctx, "", "ls-remote", projectURL+".git", "refs/heads/main", "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	if err != nil {
		return remoteVersion{}, err
	}
	remote := remoteVersion{Version: tag}
	values := map[string]string{}
	for _, line := range strings.Split(refs, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			values[fields[1]] = fields[0]
		}
	}
	remote.Main = values["refs/heads/main"]
	remote.Commit = values["refs/tags/"+tag+"^{}"]
	if remote.Commit == "" {
		remote.Commit = values["refs/tags/"+tag]
	}
	if len(remote.Commit) != 40 || len(remote.Main) != 40 {
		return remoteVersion{}, fmt.Errorf("не удалось сверить GitHub refs")
	}
	return remote, nil
}

// updateStatus возвращает сведения об установленной сборке, релизе и копиях исходников.
func (s *server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, 401, actionResponse{Message: "требуется административный токен"})
		return
	}
	if s.updates == nil {
		writeJSON(w, 503, actionResponse{Message: "проверка обновлений не настроена"})
		return
	}
	remote := s.updates.snapshot()
	if time.Since(remote.CheckedAt) > 10*time.Minute || r.Method == http.MethodPost {
		s.updates.check()
		remote = s.updates.snapshot()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	source := inspectSource(ctx, os.Getenv("HKC_SOURCE_DIR"))
	local := inspectSource(ctx, os.Getenv("HKC_LOCAL_SOURCE_DIR"))
	job := readUpdateJob()
	canInstall := runtime.GOOS == "linux" && os.Getenv("HKC_UPDATE_ENABLED") == "1"
	writeJSON(w, 200, map[string]any{"installed": currentVersion(), "source": source, "local": local, "github": remote, "enabled": canInstall, "job": job})
}

// readUpdateJob читает ограниченный по размеру файл состояния отдельной службы обновления.
func readUpdateJob() map[string]any {
	var job map[string]any
	if dir := os.Getenv("HKC_UPDATE_DIR"); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "status.json")); err == nil {
			_ = json.Unmarshal(data, &job)
		}
	}
	return job
}

// updateProgress выдаёт администратору ход установки отдельной службы.
func (s *server) updateProgress(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "требуется административный токен"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": readUpdateJob(), "installed": currentVersion()})
}

// installUpdate проверяет возможность обновления и запускает отдельную systemd-службу, которая переживёт
// перезапуск самой панели.
func (s *server) installUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, 401, actionResponse{Message: "требуется административный токен"})
		return
	}
	if runtime.GOOS != "linux" || os.Getenv("HKC_UPDATE_ENABLED") != "1" || s.updates == nil {
		writeJSON(w, 409, actionResponse{Message: "служба обновления не настроена"})
		return
	}
	remote := s.updates.snapshot()
	if remote.Error != "" || remote.Commit == "" || remote.Checking || time.Since(remote.CheckedAt) > 15*time.Minute {
		s.updates.check()
		writeJSON(w, 409, actionResponse{Message: "сначала дождитесь успешной сверки GitHub"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	for _, dir := range []string{os.Getenv("HKC_SOURCE_DIR"), os.Getenv("HKC_LOCAL_SOURCE_DIR")} {
		v := inspectSource(ctx, dir)
		if v.Configured && (v.Error != "" || v.Dirty || v.Branch != "main") {
			writeJSON(w, 409, actionResponse{Message: "локальная копия изменена, недоступна или находится не на main; обновление остановлено"})
			return
		}
	}
	command := []string{"systemctl", "start", "--no-block", "hkc-update.service"}
	if user := strings.TrimSpace(os.Getenv("HKC_SERVICE_USER")); user != "" && user != "root" {
		command = []string{"/usr/bin/sudo", "-n", "/usr/bin/systemctl", "start", "--no-block", "hkc-update.service"}
	}
	if err := exec.CommandContext(ctx, command[0], command[1:]...).Run(); err != nil {
		writeJSON(w, 500, actionResponse{Message: "не удалось запустить hkc-update.service"})
		return
	}
	writeJSON(w, http.StatusAccepted, actionResponse{OK: true, Message: "Обновление запущено. Панель перезапустится; при ошибке запуска восстановится предыдущая сборка."})
}
