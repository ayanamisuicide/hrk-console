package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

const projectURL = "https://github.com/ayanamisuicide/hrk-console"

// rawURL — адрес файлов репозитория по тегу; CHANGELOG берётся из того же тега, что и релиз.
var rawURL = "https://raw.githubusercontent.com/ayanamisuicide/hrk-console"

var buildVersion = "dev"
var buildCommit = ""
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var backupName = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,80}$`)

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

// compareVersions сравнивает теги vX.Y.Z; некорректный тег считается самым старым.
func compareVersions(a, b string) int {
	parse := func(tag string) [3]int {
		var parts [3]int
		if !releaseTag.MatchString(tag) {
			return [3]int{-1, -1, -1}
		}
		for index, field := range strings.Split(strings.TrimPrefix(tag, "v"), ".") {
			parts[index], _ = strconv.Atoi(field)
		}
		return parts
	}
	left, right := parse(a), parse(b)
	for index := range left {
		if left[index] != right[index] {
			if left[index] < right[index] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// sourceInfo — Результат проверки одной копии Git без изменения её файлов.
type sourceInfo struct {
	Configured bool   `json:"configured"`
	Path       string `json:"path,omitempty"`
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
	v := sourceInfo{Configured: true, Path: dir}
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

// releaseNotes — Раздел CHANGELOG одной версии: дата и группы пунктов («Добавлено», «Изменено»…).
type releaseNotes struct {
	Version  string         `json:"version"`
	Date     string         `json:"date,omitempty"`
	Sections []notesSection `json:"sections"`
}

type notesSection struct {
	Title string   `json:"title"`
	Items []string `json:"items"`
}

var (
	changelogVersion = regexp.MustCompile(`^##\s+\[?(\d+\.\d+\.\d+)\]?(?:\([^)]*\))?\s*(?:[—-]\s*(\S+))?`)
	markdownLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
)

// parseChangelog разбирает CHANGELOG.md в формате Keep a Changelog. Ссылки заменяются их текстом,
// многострочные пункты склеиваются; остальная разметка остаётся как есть и экранируется в браузере.
func parseChangelog(text string, limit int) []releaseNotes {
	releases := []releaseNotes{}
	var current *releaseNotes
	var section *notesSection
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if match := changelogVersion.FindStringSubmatch(line); match != nil {
			if len(releases) >= limit {
				break
			}
			releases = append(releases, releaseNotes{Version: "v" + match[1], Date: match[2], Sections: []notesSection{}})
			current, section = &releases[len(releases)-1], nil
			continue
		}
		if current == nil {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "### "):
			current.Sections = append(current.Sections, notesSection{Title: strings.TrimSpace(trimmed[4:]), Items: []string{}})
			section = &current.Sections[len(current.Sections)-1]
		case strings.HasPrefix(trimmed, "- ") && section != nil:
			section.Items = append(section.Items, markdownLink.ReplaceAllString(strings.TrimSpace(trimmed[2:]), "$1"))
		case trimmed != "" && section != nil && len(section.Items) > 0 && strings.HasPrefix(line, "  "):
			section.Items[len(section.Items)-1] += " " + markdownLink.ReplaceAllString(trimmed, "$1")
		}
	}
	return releases
}

// remoteVersion — Стабильный тег релиза, подтверждённый коммит и заметки о версиях.
type remoteVersion struct {
	Version   string         `json:"version,omitempty"`
	Commit    string         `json:"commit,omitempty"`
	Main      string         `json:"main,omitempty"`
	CheckedAt time.Time      `json:"checkedAt"`
	Error     string         `json:"error,omitempty"`
	Checking  bool           `json:"checking"`
	Releases  []releaseNotes `json:"-"`
}

// updateChecker — Последний результат фоновой проверки релиза, защищённый от параллельных обращений.
// onNewVersion вызывается один раз для каждой найденной версии новее установленной.
type updateChecker struct {
	mu           sync.Mutex
	remote       remoteVersion
	notified     string
	onNewVersion func(version string, notes []releaseNotes)
	fetch        func(ctx context.Context) (remoteVersion, error)
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
	if c.remote.Checking || time.Since(c.remote.CheckedAt) < 30*time.Second {
		c.mu.Unlock()
		return
	}
	c.remote.Checking = true
	fetch := c.fetch
	if fetch == nil {
		fetch = fetchRemoteVersion
	}
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		remote, err := fetch(ctx)
		remote.CheckedAt = time.Now().UTC()
		if err != nil {
			remote.Error = err.Error()
		}
		c.mu.Lock()
		c.remote = remote
		announce := err == nil && remote.Version != c.notified && compareVersions(remote.Version, currentVersion().Version) > 0
		if announce {
			c.notified = remote.Version
		}
		callback := c.onNewVersion
		c.mu.Unlock()
		if announce && callback != nil {
			callback(remote.Version, remote.Releases)
		}
	}()
}

// fetchRemoteVersion получает стабильный тег через HTTPS-переадресацию релиза, сверяет его коммит
// по ссылкам Git и загружает CHANGELOG этого тега. Ошибка заметок не мешает обновлению.
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
		return remoteVersion{}, errors.New("GitHub недоступен — проверьте интернет на сервере")
	}
	response.Body.Close()
	prefix := projectURL + "/releases/tag/"
	finalURL := response.Request.URL.String()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(finalURL, prefix) {
		return remoteVersion{}, fmt.Errorf("GitHub не отдал стабильный релиз (HTTP %d)", response.StatusCode)
	}
	tag := strings.TrimPrefix(finalURL, prefix)
	if !releaseTag.MatchString(tag) {
		return remoteVersion{}, fmt.Errorf("неподдерживаемый тег релиза")
	}
	refs, err := gitOutput(ctx, "", "ls-remote", projectURL+".git", "refs/heads/main", "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	if err != nil {
		return remoteVersion{}, errors.New("не удалось сверить релиз с репозиторием GitHub")
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
	if len(remote.Commit) != 40 {
		return remoteVersion{}, fmt.Errorf("не удалось сверить коммит релиза")
	}
	remote.Releases = fetchChangelog(ctx, tag)
	return remote, nil
}

func fetchChangelog(ctx context.Context, tag string) []releaseNotes {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL+"/"+tag+"/CHANGELOG.md", nil)
	if err != nil {
		return nil
	}
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil
	}
	return parseChangelog(string(data), 40)
}

// backupInfo — Резервная сборка из публичного индекса службы обновления.
type backupInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	CreatedAt string `json:"createdAt"`
	Size      int64  `json:"size"`
}

func readBackups() []backupInfo {
	result := []backupInfo{}
	dir := os.Getenv("HKC_UPDATE_DIR")
	if dir == "" {
		return result
	}
	data, err := os.ReadFile(filepath.Join(dir, "backups.json"))
	if err != nil || len(data) > 1<<20 {
		return result
	}
	var index struct {
		Backups []backupInfo `json:"backups"`
	}
	if json.Unmarshal(data, &index) == nil {
		for _, item := range index.Backups {
			if backupName.MatchString(item.Name) {
				result = append(result, item)
			}
		}
	}
	return result
}

// readUpdateJob читает ограниченный по размеру файл состояния отдельной службы обновления.
func readUpdateJob() map[string]any {
	var job map[string]any
	if dir := os.Getenv("HKC_UPDATE_DIR"); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "status.json")); err == nil && len(data) < 1<<20 {
			_ = json.Unmarshal(data, &job)
		}
	}
	return job
}

func jobRunning(job map[string]any) bool {
	phase, _ := job["phase"].(string)
	if phase != "checking" && phase != "downloading" && phase != "restarting" {
		return false
	}
	// Зависшее задание (служба упала, не записав итог) не блокирует новое бесконечно.
	updated, _ := job["updatedAt"].(string)
	at, err := time.Parse(time.RFC3339Nano, updated)
	return err != nil || time.Since(at) < 15*time.Minute
}

// updateIssue — Понятная причина, по которой установка недоступна, и что сделать.
type updateIssue struct {
	Text string `json:"text"`
	Fix  string `json:"fix,omitempty"`
}

type updateOverview struct {
	Installed       versionInfo    `json:"installed"`
	Latest          remoteVersion  `json:"latest"`
	UpdateAvailable bool           `json:"updateAvailable"`
	Enabled         bool           `json:"enabled"`
	Releases        []releaseNotes `json:"releases"`
	Backups         []backupInfo   `json:"backups"`
	Job             map[string]any `json:"job"`
	Running         bool           `json:"running"`
	Blockers        []updateIssue  `json:"blockers"`
	Sources         []sourceInfo   `json:"sources"`
}

// buildUpdateOverview собирает всё для страницы обновлений одним ответом.
func (s *server) buildUpdateOverview(ctx context.Context) updateOverview {
	installed := currentVersion()
	remote := s.updates.snapshot()
	overview := updateOverview{Installed: installed, Latest: remote, Releases: []releaseNotes{}, Backups: readBackups(),
		Job: readUpdateJob(), Blockers: []updateIssue{}, Sources: []sourceInfo{},
		Enabled: runtime.GOOS == "linux" && os.Getenv("HKC_UPDATE_ENABLED") == "1"}
	overview.Running = jobRunning(overview.Job)
	overview.UpdateAvailable = remote.Error == "" && compareVersions(remote.Version, installed.Version) > 0
	if remote.Releases != nil {
		overview.Releases = remote.Releases
	}
	for _, dir := range []string{os.Getenv("HKC_SOURCE_DIR"), os.Getenv("HKC_LOCAL_SOURCE_DIR")} {
		if info := inspectSource(ctx, dir); info.Configured {
			overview.Sources = append(overview.Sources, info)
		}
	}
	if !overview.Enabled {
		overview.Blockers = append(overview.Blockers, updateIssue{
			Text: "Установка из панели не настроена на этом сервере.",
			Fix:  "Запустите установщик: он настроит службу обновлений и сохранит ваши данные.",
		})
	}
	if installed.Modified {
		overview.Blockers = append(overview.Blockers, updateIssue{
			Text: "Панель собрана из изменённых исходников.",
			Fix:  "Соберите её из чистой копии или установите релиз установщиком.",
		})
	}
	if len(overview.Sources) > 0 {
		main := overview.Sources[0]
		switch {
		case main.Error != "":
			overview.Blockers = append(overview.Blockers, updateIssue{Text: "Не удаётся прочитать исходники панели в " + main.Path + ".", Fix: "Проверьте права доступа к каталогу."})
		case main.Dirty:
			overview.Blockers = append(overview.Blockers, updateIssue{Text: "В исходниках панели (" + main.Path + ") есть несохранённые изменения.", Fix: "Сохраните их в коммит или отмените: обновление их не трогает."})
		case main.Branch != "main":
			overview.Blockers = append(overview.Blockers, updateIssue{Text: "Исходники панели не на ветке main.", Fix: "git -C " + main.Path + " checkout main"})
		}
	}
	return overview
}

// updateStatus возвращает сведения для страницы обновлений; давняя проверка запускается заново.
func (s *server) updateStatus(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, 401, actionResponse{Message: "требуется административный токен"})
		return
	}
	if s.updates == nil {
		writeJSON(w, 503, actionResponse{Message: "проверка обновлений не настроена"})
		return
	}
	if time.Since(s.updates.snapshot().CheckedAt) > 10*time.Minute || r.Method == http.MethodPost {
		s.updates.check()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.buildUpdateOverview(ctx))
}

// updateProgress выдаёт администратору ход работы службы; страница опрашивает его и во время перезапуска.
func (s *server) updateProgress(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "требуется административный токен"})
		return
	}
	job := readUpdateJob()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"job": job, "running": jobRunning(job), "installed": currentVersion()})
}

// updateRequest — Задание для службы обновления; пишется в каталог данных панели.
type updateRequest struct {
	Action      string `json:"action"`
	Version     string `json:"version,omitempty"`
	Backup      string `json:"backup,omitempty"`
	RequestedAt string `json:"requestedAt"`
}

func (s *server) updateRequestPath() string {
	if path := os.Getenv("HKC_UPDATE_REQUEST_FILE"); path != "" {
		return path
	}
	return filepath.Join(s.dataDir, "update-request.json")
}

// startUpdateJob проверяет готовность, оставляет задание и запускает службу hkc-update.
func (s *server) startUpdateJob(w http.ResponseWriter, r *http.Request, request updateRequest, audit string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	overview := s.buildUpdateOverview(ctx)
	if len(overview.Blockers) > 0 {
		writeJSON(w, http.StatusConflict, actionResponse{Message: overview.Blockers[0].Text + " " + overview.Blockers[0].Fix})
		return
	}
	if overview.Running {
		writeJSON(w, http.StatusConflict, actionResponse{Message: "обновление уже выполняется"})
		return
	}
	request.RequestedAt = time.Now().UTC().Format(time.RFC3339)
	data, _ := json.Marshal(request)
	path := s.updateRequestPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		writeJSON(w, 500, actionResponse{Message: "не удалось подготовить задание"})
		return
	}
	if err := writePrivateAtomic(path, data); err != nil {
		writeJSON(w, 500, actionResponse{Message: "не удалось записать задание для службы обновления"})
		return
	}
	if err := startUpdateService(ctx); err != nil {
		_ = os.Remove(path)
		writeJSON(w, 500, actionResponse{Message: "не удалось запустить службу hkc-update"})
		return
	}
	s.record(r, "admin", audit, request.Version+request.Backup)
	writeJSON(w, http.StatusAccepted, actionResponse{OK: true, Message: "Запущено. Панель перезапустится, страница переподключится сама."})
}

// startUpdateService запускает отдельную службу; пользователь панели не root вызывает её
// через узкое правило sudo, которое создаёт установщик. Переменная подменяется в тестах.
var startUpdateService = func(ctx context.Context) error {
	command := []string{"systemctl", "start", "--no-block", "hkc-update.service"}
	if user := strings.TrimSpace(os.Getenv("HKC_SERVICE_USER")); user != "" && user != "root" {
		command = []string{"/usr/bin/sudo", "-n", "/usr/bin/systemctl", "start", "--no-block", "hkc-update.service"}
	}
	return exec.CommandContext(ctx, command[0], command[1:]...).Run()
}

func decodeUpdateBody(r *http.Request, target any) error {
	if r.ContentLength == 0 {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// installUpdate ставит выбранную версию; без версии — последний стабильный релиз.
func (s *server) installUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, 401, actionResponse{Message: "требуется административный токен"})
		return
	}
	if s.updates == nil {
		writeJSON(w, 409, actionResponse{Message: "служба обновления не настроена"})
		return
	}
	var input struct {
		Version string `json:"version"`
	}
	if err := decodeUpdateBody(r, &input); err != nil {
		writeJSON(w, 400, actionResponse{Message: "некорректный запрос"})
		return
	}
	if input.Version == "" {
		remote := s.updates.snapshot()
		if remote.Error != "" || remote.Version == "" {
			s.updates.check()
			writeJSON(w, 409, actionResponse{Message: "сначала дождитесь проверки GitHub"})
			return
		}
		input.Version = remote.Version
	}
	if !releaseTag.MatchString(input.Version) {
		writeJSON(w, 400, actionResponse{Message: "версия должна быть в формате vX.Y.Z"})
		return
	}
	s.startUpdateJob(w, r, updateRequest{Action: "install", Version: input.Version}, "update.install")
}

// rollbackUpdate возвращает резервную сборку из списка службы обновления.
func (s *server) rollbackUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, 401, actionResponse{Message: "требуется административный токен"})
		return
	}
	var input struct {
		Backup string `json:"backup"`
	}
	if err := decodeUpdateBody(r, &input); err != nil || !backupName.MatchString(input.Backup) {
		writeJSON(w, 400, actionResponse{Message: "выберите резервную сборку"})
		return
	}
	found := false
	for _, item := range readBackups() {
		found = found || item.Name == input.Backup
	}
	if !found {
		writeJSON(w, 404, actionResponse{Message: "резервная сборка не найдена"})
		return
	}
	s.startUpdateJob(w, r, updateRequest{Action: "rollback", Backup: input.Backup}, "update.rollback")
}
