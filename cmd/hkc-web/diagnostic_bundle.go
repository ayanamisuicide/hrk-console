package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"heroku-console/botproc"
	"heroku-console/logfeed"
)

var diagnosticSecrets = []*regexp.Regexp{
	regexp.MustCompile(`(?i)hkc\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`),
	regexp.MustCompile(`(?i)(bot|api|admin)[_-]?token\s*[:=]\s*[^\s,;]+`),
	regexp.MustCompile(`(?i)(redis|postgres(?:ql)?|mysql)://[^\s/@:]+:[^\s/@]+@`),
}

// redactDiagnostic скрывает известные форматы токенов и паролей в адресах баз. Это фильтр известных
// шаблонов, а не гарантия удаления любого возможного секрета.
func redactDiagnostic(value string) string {
	value = diagnosticSecrets[0].ReplaceAllString(value, "hkc.[REDACTED]")
	value = diagnosticSecrets[1].ReplaceAllStringFunc(value, func(match string) string {
		if index := strings.IndexAny(match, ":="); index >= 0 {
			return match[:index+1] + "[REDACTED]"
		}
		return "[REDACTED]"
	})
	return diagnosticSecrets[2].ReplaceAllString(value, "$1://[REDACTED]@")
}

// addJSONToBundle кодирует именованный диагностический объект и добавляет его в ZIP.
func addJSONToBundle(archive *zip.Writer, name string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	entry, err := archive.Create(name)
	if err != nil {
		return err
	}
	_, err = entry.Write(append(data, '\n'))
	return err
}

// addTextToBundle маскирует известные секреты в строках перед добавлением текстового файла в ZIP.
func addTextToBundle(archive *zip.Writer, name string, lines []string) error {
	entry, err := archive.Create(name)
	if err != nil {
		return err
	}
	for index := range lines {
		lines[index] = redactDiagnostic(lines[index])
	}
	_, err = entry.Write([]byte(strings.Join(lines, "\n")))
	return err
}

// diagnosticBundle собирает административный ZIP со сводками и ограниченными хвостами журналов; значения
// конфигурации в него не включаются.
func (s *server) diagnosticBundle(w http.ResponseWriter, r *http.Request) {
	if !s.adminAuthorized(r) {
		writeJSON(w, http.StatusUnauthorized, actionResponse{Message: "неверный административный токен"})
		return
	}

	pid := s.bot.PID()
	s.systemMu.RLock()
	system := s.latestSystem
	s.systemMu.RUnlock()
	if system.SampledAt.IsZero() {
		system = readSystemStatus(s.bot.HerokuDir)
	}
	s.configMu.Lock()
	config, configErr := s.readConfigLocked()
	s.configMu.Unlock()
	configured := make(map[string]bool, len(herokuConfigKeys))
	for _, key := range herokuConfigKeys {
		_, configured[key] = config[key]
	}
	audit := []auditEvent{}
	var auditErr error
	if s.audit != nil {
		audit, auditErr = s.audit.recent(100)
	}

	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	errorsFound := make([]string, 0, 2)
	if configErr != nil {
		errorsFound = append(errorsFound, "config: "+configErr.Error())
	}
	if auditErr != nil {
		errorsFound = append(errorsFound, "audit: "+auditErr.Error())
	}
	items := []struct {
		name  string
		value any
	}{
		{"version.json", currentVersion()},
		{"status.json", statusResponse{Running: pid != 0, PID: pid, Uptime: botproc.Uptime(pid), Version: s.bot.Version(), HerokuDir: s.bot.HerokuDir}},
		{"system.json", system},
		{"config-summary.json", map[string]any{"configured": configured, "path": s.configPath()}},
		{"audit.json", audit},
		{"errors.json", errorsFound},
	}
	for _, item := range items {
		if err := addJSONToBundle(archive, item.name, item.value); err != nil {
			writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось собрать диагностический архив"})
			return
		}
	}
	if err := addTextToBundle(archive, "bot.log", logfeed.TailLines(s.bot.LogFile, 500)); err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось собрать диагностический архив"})
		return
	}
	if err := addTextToBundle(archive, "startup.log", logfeed.TailLines(s.bot.StartupLog, 100)); err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось собрать диагностический архив"})
		return
	}
	if err := archive.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, actionResponse{Message: "не удалось завершить диагностический архив"})
		return
	}

	s.record(r, "admin", "diagnostics.bundle", "downloaded")
	name := fmt.Sprintf("hkc-diagnostics-%s.zip", time.Now().UTC().Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", fmt.Sprintf("%d", buffer.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buffer.Bytes())
}
