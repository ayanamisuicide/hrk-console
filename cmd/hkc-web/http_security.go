package main

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// loopbackAddress проверяет, привязан ли адрес с портом к локальному интерфейсу.
func loopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return host == "localhost" || net.ParseIP(host).IsLoopback()
}

// securityHeaders ограничивает размер тела запроса и задаёт защитные заголовки. Ответы API не кешируются;
// HSTS отправляется только для защищённого запроса.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if strings.HasPrefix(r.URL.Path, "/tg/") {
			// Мини-приложение: скрипт моста Telegram и встраивание во фрейм только для веб-клиента Telegram.
			w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"script-src 'self' https://telegram.org; frame-ancestors https://web.telegram.org https://*.telegram.org")
		} else {
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self'; script-src 'self'")
		}
		if secureRequest(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON задаёт код HTTP и кодирует ответ как JSON в UTF-8.
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
