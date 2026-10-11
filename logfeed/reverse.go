package logfeed

import (
	"io"
	"os"
	"strings"
)

// reverseBlock — размер блока при чтении файла с конца.
const reverseBlock = 256 * 1024

// eachEntryBackwards отдаёт записи файла от последней к первой, читая его блоками с конца,
// и останавливается, когда yield вернул false. Память ограничена блоком и одной записью, а
// не размером файла; поэтому свежие записи доступны сразу, а редкий поиск не раздувает
// процесс. Записи приходят без ID: его считает тот, кому запись понадобилась (см. seal).
// Строки записи — срезы одного блока: кто оставляет запись себе, копирует её через detach,
// иначе блок остаётся в памяти. Результат совпадает с прямым разбором в обратном порядке.
func eachEntryBackwards(path string, block int, yield func(Entry) bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return
	}
	if block <= 0 {
		block = reverseBlock
	}
	var (
		pos   = info.Size()
		carry []byte   // начало строки, которая продолжается в предыдущем блоке
		cont  []string // продолжения записи в обратном порядке
		buf   = make([]byte, block)
		stop  bool
	)
	// line разбирает одну законченную строку; false — читать дальше не нужно.
	line := func(text string) bool {
		text = strings.TrimRight(text, "\r")
		if e, ok := ParseLine(text); ok {
			if n := len(cont); n > 0 {
				e.Trace = make([]string, n)
				for i, c := range cont {
					e.Trace[n-1-i] = c
				}
			}
			cont = cont[:0]
			return yield(e)
		}
		cont = append(cont, text)
		return true
	}
	first := true // самый конец файла: завершающий '\n' не начинает пустую строку
	for pos > 0 && !stop {
		start := pos - int64(block)
		if start < 0 {
			start = 0
		}
		chunk := buf[:pos-start]
		if _, err := f.ReadAt(chunk, start); err != nil && err != io.EOF {
			return
		}
		// Блок и недочитанное начало строки справа — одной строкой на весь блок: записи
		// ссылаются на неё срезами, без отдельной аллокации на каждую строку.
		data := string(chunk)
		if len(carry) > 0 {
			data += string(carry)
		}
		end := len(data)
		if first && end > 0 && data[end-1] == '\n' {
			end--
		}
		first = false
		for end > 0 {
			i := strings.LastIndexByte(data[:end], '\n')
			if i < 0 {
				break
			}
			if !line(data[i+1 : end]) {
				stop = true
				break
			}
			end = i
		}
		if stop {
			break
		}
		// Остаток слева — начало строки, которая может продолжаться в блоке левее.
		carry = append(carry[:0], data[:end]...)
		pos = start
		if pos == 0 {
			if !line(string(carry)) {
				return
			}
			carry = nil
			break
		}
	}
	if stop {
		return
	}
	// Строки до первого префикса образуют одну запись уровня OTHER, как в Grouper.
	if n := len(cont); n > 0 {
		e := Entry{Level: "OTHER", Message: cont[n-1]}
		for i := n - 2; i >= 0; i-- {
			e.Trace = append(e.Trace, cont[i])
		}
		yield(e)
	}
}
