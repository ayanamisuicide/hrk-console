package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testBotToken = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdef123"

// signInitData собирает initData так же, как клиент Telegram.
func signInitData(token string, userID int64, at time.Time) string {
	values := url.Values{}
	values.Set("auth_date", strconv.FormatInt(at.Unix(), 10))
	values.Set("query_id", "AAE")
	values.Set("user", `{"id":`+strconv.FormatInt(userID, 10)+`,"first_name":"Аня","username":"anya"}`)
	keys := []string{}
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := []string{}
	for _, key := range keys {
		lines = append(lines, key+"="+values.Get(key))
	}
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	mac := hmac.New(sha256.New, secret.Sum(nil))
	mac.Write([]byte(strings.Join(lines, "\n")))
	values.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return values.Encode()
}

// TestVerifyTelegramInitData принимает подпись Telegram и отвергает подделку и старые данные.
func TestVerifyTelegramInitData(t *testing.T) {
	now := time.Now()
	user, err := VerifyInitData(signInitData(testBotToken, 7, now), testBotToken, now)
	if err != nil || user.ID != 7 || user.Username != "anya" {
		t.Fatalf("valid data rejected: %v %+v", err, user)
	}
	if _, err := VerifyInitData(signInitData("999:other", 7, now), testBotToken, now); err == nil {
		t.Fatal("foreign signature accepted")
	}
	tampered := strings.Replace(signInitData(testBotToken, 7, now), "anya", "evil", 1)
	if _, err := VerifyInitData(tampered, testBotToken, now); err == nil {
		t.Fatal("tampered data accepted")
	}
	if _, err := VerifyInitData(signInitData(testBotToken, 7, now.Add(-48*time.Hour)), testBotToken, now); err == nil {
		t.Fatal("stale data accepted")
	}
}
