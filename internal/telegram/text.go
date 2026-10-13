package telegram

import (
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

// Ответы по частям: черновик sendMessageDraft показывает начало HTML-текста, которое должно
// оставаться корректной разметкой.

// draftCounter даёт неповторяющиеся ненулевые идентификаторы черновиков: одинаковый
// draft_id анимирует обновления, разные — заменяют черновик без анимации.
var draftCounter atomic.Int64

// NextDraftID — новый идентификатор черновика ответа.
func NextDraftID() int64 {
	return draftCounter.Add(1)<<8 | 1
}

// HTMLPrefix возвращает начало HTML-текста длиной visible видимых символов. Открытые теги
// закрываются, а HTML-сущности (&amp;) не рвутся посередине: иначе Telegram отклонил бы
// черновик. toWord продлевает обрезку до конца слова, чтобы текст набирался словами.
func HTMLPrefix(src string, visible int, toWord bool) string {
	var out strings.Builder
	var open []string
	count := 0
	i := 0
	extra := 0
	for i < len(src) {
		if count >= visible {
			if !toWord || extra > 24 {
				break
			}
		}
		c := src[i]
		if c == '<' {
			end := strings.IndexByte(src[i:], '>')
			if end < 0 {
				break
			}
			tag := src[i : i+end+1]
			fields := strings.Fields(strings.Trim(tag, "<>/"))
			if len(fields) == 0 {
				out.WriteString(tag)
				i += end + 1
				continue
			}
			name := strings.ToLower(fields[0])
			if strings.HasPrefix(tag, "</") {
				for k := len(open) - 1; k >= 0; k-- {
					if open[k] == name {
						open = append(open[:k], open[k+1:]...)
						break
					}
				}
			} else if !strings.HasSuffix(tag, "/>") {
				open = append(open, name)
			}
			out.WriteString(tag)
			i += end + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(src[i:])
		unit := size
		if c == '&' {
			if semi := strings.IndexByte(src[i:], ';'); semi > 0 && semi <= 10 {
				out.WriteString(src[i : i+semi+1])
				i += semi + 1
				count++
				if count > visible {
					extra++
				}
				continue
			}
		}
		if count >= visible && (r == ' ' || r == '\n') {
			break
		}
		out.WriteString(src[i : i+unit])
		i += unit
		count++
		if count > visible {
			extra++
		}
	}
	for k := len(open) - 1; k >= 0; k-- {
		out.WriteString("</" + open[k] + ">")
	}
	return out.String()
}

// HTMLVisibleLen считает видимые символы HTML-текста: теги не в счёт, сущность — один символ.
func HTMLVisibleLen(src string) int {
	count := 0
	for i := 0; i < len(src); {
		switch src[i] {
		case '<':
			end := strings.IndexByte(src[i:], '>')
			if end < 0 {
				return count
			}
			i += end + 1
		case '&':
			if semi := strings.IndexByte(src[i:], ';'); semi > 0 && semi <= 10 {
				i += semi + 1
			} else {
				i++
			}
			count++
		default:
			_, size := utf8.DecodeRuneInString(src[i:])
			i += size
			count++
		}
	}
	return count
}
