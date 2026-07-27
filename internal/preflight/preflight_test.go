package preflight

import "testing"

func TestParseTestJSON(t *testing.T) {
	out := []byte(`{"Action":"run","Package":"a","Test":"TestOne"}
{"Action":"pass","Package":"a","Test":"TestOne"}
{"Action":"run","Package":"a","Test":"TestTwo"}
{"Action":"pass","Package":"a","Test":"TestTwo"}
{"Action":"pass","Package":"a"}
{"Action":"pass","Package":"b","Test":"TestThree"}
{"Action":"pass","Package":"b"}
`)
	r := parseTestJSON(out)
	if r.passed != 3 {
		t.Errorf("пройдено тестов: got %d, want 3", r.passed)
	}
	if r.packages != 2 {
		t.Errorf("пакетов: got %d, want 2", r.packages)
	}
	if r.failed != 0 {
		t.Errorf("упавших быть не должно, got %d", r.failed)
	}
}

func TestParseTestJSONFailure(t *testing.T) {
	out := []byte(`{"Action":"pass","Package":"a","Test":"TestOK"}
{"Action":"fail","Package":"a","Test":"TestBroken"}
{"Action":"fail","Package":"a"}
`)
	r := parseTestJSON(out)
	if r.failed != 1 || r.firstFail != "TestBroken" {
		t.Errorf("упавший тест: got %d/%q, want 1/TestBroken", r.failed, r.firstFail)
	}
	// Упавший пакет не считается пройденным.
	if r.packages != 0 {
		t.Errorf("пакетов: got %d, want 0", r.packages)
	}
}

// В поток go test попадают и строки мимо протокола — паника рантайма,
// вывод сборки. Разбор должен их пережить, а не оборваться на первой.
func TestParseTestJSONSurvivesGarbage(t *testing.T) {
	out := []byte(`# heroku-console/internal/tui
не JSON вовсе
{"Action":"pass","Package":"a","Test":"TestOne"}
{"Action":"pass","Package":"a"}
`)
	if r := parseTestJSON(out); r.passed != 1 || r.packages != 1 {
		t.Errorf("мусор в потоке сбил разбор: %+v", r)
	}
}

func TestPlural(t *testing.T) {
	cases := map[int]string{
		1: "1 тест", 2: "2 теста", 4: "4 теста", 5: "5 тестов",
		11: "11 тестов", 21: "21 тест", 22: "22 теста", 25: "25 тестов",
		101: "101 тест", 111: "111 тестов",
	}
	for n, want := range cases {
		if got := plural(n, "тест", "теста", "тестов"); got != want {
			t.Errorf("plural(%d): got %q, want %q", n, got, want)
		}
	}
}
