package setup

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"heroku-console/internal/theme"
)

// Загруженные модули бота живут своей жизнью: их ставят по ссылке через
// .dlmod, requirements.txt бота про их зависимости ничего не знает, а сам
// загрузчик доставляет только то, что модуль объявил заголовком
// `# requires:`. Модуль, который просто делает `import httpx` без заголовка,
// падает при каждом старте с ModuleNotFoundError — и так до тех пор, пока
// человек не прочитает трейсбек в логе и не поставит пакет руками.
//
// Здесь эта работа делается сама: разбираем импорты загруженных модулей,
// оставляем те, что не резолвятся в venv бота, и ставим их.

// DepScanScript ищет недостающие зависимости загруженных модулей. Запускается
// python'ом из venv бота — именно его набор пакетов и надо проверять.
//
// Импорты берутся из AST, а не регуляркой по тексту: `import` внутри строки
// или закомментированный не должен превращаться в попытку что-то поставить.
// Заголовки `# requires:` читаются отдельно регуляркой — это не код, в AST
// их нет, и имена там сразу пакетные, без перевода.
//
// Экспортирован (а не только внутренний вызов через scanModuleDeps) —
// remotebot гоняет его же по SSH для того же экрана проверок в удалённом
// режиме, дублировать полсотни строк разбора AST ради этого не стоило.
const DepScanScript = `
import ast, importlib.metadata, importlib.util, pathlib, re, sys

modules_dir = pathlib.Path(sys.argv[1])
imports, declared = set(), set()

for path in sorted(modules_dir.glob("*.py")):
    try:
        src = path.read_text(encoding="utf-8", errors="replace")
        tree = ast.parse(src)
    except Exception:
        continue  # битый или несовместимый по синтаксису модуль — не наша забота
    for m in re.finditer(r"^#\s*requires:(.+)$", src, re.M):
        declared.update(m.group(1).split())
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                imports.add(alias.name.split(".")[0])
        elif isinstance(node, ast.ImportFrom):
            # level > 0 — относительный импорт внутри пакета бота, не пакет с PyPI
            if node.level == 0 and node.module:
                imports.add(node.module.split(".")[0])

stdlib = set(sys.stdlib_module_names)

def missing(name):
    if name in stdlib or name.startswith("_"):
        return False
    try:
        return importlib.util.find_spec(name) is None
    except Exception:
        # найтись не смогло, но и уверенности, что пакета нет, тоже нет
        return False

def dist_missing(name):
    # Имя из заголовка requires — пакетное, а не импортное: pillow
    # импортируется как PIL, и find_spec("pillow") соврал бы "не установлен".
    # Спрашиваем у метаданных установленных дистрибутивов.
    try:
        importlib.metadata.distribution(name)
        return False
    except Exception:
        return True

for name in sorted(imports):
    if missing(name):
        print("import", name)
for name in sorted(declared):
    if dist_missing(name) and missing(name.replace("-", "_")):
        print("pip", name)
`

// pipName переводит имя импорта в имя пакета на PyPI там, где они не
// совпадают: ставить надо pillow, а импортируется PIL. Список короткий
// намеренно — только то, что реально встречается в модулях Heroku; для
// остальных имя импорта и есть имя пакета.
var pipName = map[string]string{
	"PIL":                "pillow",
	"bs4":                "beautifulsoup4",
	"yaml":               "PyYAML",
	"cv2":                "opencv-python-headless",
	"dotenv":             "python-dotenv",
	"git":                "GitPython",
	"dateutil":           "python-dateutil",
	"magic":              "python-magic",
	"Crypto":             "pycryptodome",
	"OpenSSL":            "pyOpenSSL",
	"serial":             "pyserial",
	"skimage":            "scikit-image",
	"sklearn":            "scikit-learn",
	"fitz":               "PyMuPDF",
	"docx":               "python-docx",
	"pptx":               "python-pptx",
	"usb":                "pyusb",
	"requests_toolbelt":  "requests-toolbelt",
	"speech_recognition": "SpeechRecognition",
	"telebot":            "pyTelegramBotAPI",
}

// skipImports — имена, которые импортом выглядят, а ставить их не надо.
//
// Первые три — сам бот и его каталоги. Остальные подменяет на лету сам
// загрузчик (patched_import в heroku/loader.py): модули пишутся под
// оригинальные Telethon и Hikka, а импорт разворачивается в herokutl и
// heroku. Пакета telethon в venv нет и быть не должно — поставить его
// значило бы притащить рядом с форком оригинал, из которого модули потом
// брали бы половину классов.
var skipImports = map[string]bool{
	"heroku":         true,
	"hikka":          true,
	"loaded_modules": true,
	"telethon":       true,
	"hikkatl":        true,
}

// depCache хранит результат разбора: скрипт запускается на каждом старте
// консоли, а внутри одного запуска ответ спрашивают дважды — автонастройка и
// проверки. Указатель, а не срез, чтобы отличать "ещё не считали" от
// "посчитали, ничего не нужно".
var depCache *[]string

func invalidateDepCache() { depCache = nil }

// MissingModuleDeps возвращает пакеты, которых не хватает загруженным модулям
// бота, уже в виде имён для pip. Пустой результат — всё на месте либо
// проверять нечем (нет venv или каталога модулей).
func MissingModuleDeps(dir string) []string {
	if depCache != nil {
		return *depCache
	}
	list := scanModuleDeps(dir)
	depCache = &list
	return list
}

func scanModuleDeps(dir string) []string {
	python := filepath.Join(dir, "venv", "bin", "python")
	modules := filepath.Join(dir, "loaded_modules")
	if _, err := os.Stat(python); err != nil {
		return nil
	}
	if info, err := os.Stat(modules); err != nil || !info.IsDir() {
		return nil
	}

	cmd := exec.Command(python, "-c", DepScanScript, modules)
	// cwd = каталог бота: так find_spec видит сам пакет heroku и его
	// соседей, и они не попадают в список "не хватает".
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return ParseDepOutput(string(out))
}

// ParseDepOutput разбирает вывод DepScanScript: строки вида "import httpx" и
// "pip pillow". Имена из импортов переводятся в пакетные, объявленные
// заголовком `# requires:` берутся как есть — там уже имя пакета.
// Экспортирован по той же причине, что и сам скрипт, — см. комментарий там.
func ParseDepOutput(out string) []string {
	seen := make(map[string]bool)
	var missing []string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		kind, name, ok := strings.Cut(strings.TrimSpace(sc.Text()), " ")
		if !ok || name == "" {
			continue
		}
		if kind == "import" {
			if skipImports[name] {
				continue
			}
			if mapped, found := pipName[name]; found {
				name = mapped
			}
		}
		if !seen[name] {
			seen[name] = true
			missing = append(missing, name)
		}
	}
	return missing
}

// failedDepsFile хранит пакеты, установка которых уже проваливалась, в
// каталоге бота — рядом с venv, который они не смогли пополнить.
const failedDepsFile = ".hkc-failed-deps.json"

// failedDepRetry — как долго не трогать пакет, который уже не встал. Имя
// пакета выведено из импорта и может не существовать на PyPI вовсе — без
// паузы pip пытался бы поставить его заново на каждом запуске консоли,
// замедляя старт одной и той же безнадёжной ошибкой. Сутки достаточно редко,
// чтобы не мешать, и достаточно часто, чтобы подхватить пакет, который
// просто ещё не был опубликован или временно не доехал по сети.
const failedDepRetry = 24 * time.Hour

type failedDep struct {
	At time.Time `json:"at"`
}

func failedDepsPath(dir string) string { return filepath.Join(dir, failedDepsFile) }

// loadFailedDeps читает список ранее неудавшихся пакетов. Отсутствие файла
// или битый JSON — тот же случай, что и "ничего не пробовали": паузы ждать
// не от чего, и ensureModuleDeps попробует поставить всё заново.
func loadFailedDeps(dir string) map[string]failedDep {
	data, err := os.ReadFile(failedDepsPath(dir))
	if err != nil {
		return nil
	}
	var m map[string]failedDep
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m
}

func saveFailedDeps(dir string, m map[string]failedDep) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(failedDepsPath(dir), data, 0o644)
}

func ensureModuleDeps(dir string) {
	missing := MissingModuleDeps(dir)
	if len(missing) == 0 {
		return
	}

	failed := loadFailedDeps(dir)
	var toInstall, skipped []string
	for _, pkg := range missing {
		if fd, ok := failed[pkg]; ok && time.Since(fd.At) < failedDepRetry {
			skipped = append(skipped, pkg)
			continue
		}
		toInstall = append(toInstall, pkg)
	}
	if len(skipped) > 0 {
		fmt.Println(theme.Faint.Render("  раньше не встали, пропускаю до истечения паузы: " + strings.Join(skipped, " ")))
	}
	if len(toInstall) == 0 {
		return
	}
	step("модулям не хватает библиотек: " + strings.Join(toInstall, " "))

	// Ставим по одному, а не одним вызовом pip: имя пакета угадано по
	// импорту и может не совпасть с тем, что есть на PyPI. Одна такая
	// промашка не должна утащить за собой установку всех остальных.
	pip := filepath.Join(dir, "venv", "bin", "pip")
	var stillFailed []string
	for _, pkg := range toInstall {
		if run(pip, "install", pkg) {
			delete(failed, pkg)
			continue
		}
		stillFailed = append(stillFailed, pkg)
		if failed == nil {
			failed = make(map[string]failedDep)
		}
		failed[pkg] = failedDep{At: time.Now()}
	}
	saveFailedDeps(dir, failed)

	invalidateDepCache() // после установки список устарел
	if len(stillFailed) == 0 {
		result(true, "библиотеки установлены", "")
		return
	}
	result(false, "", "не удалось поставить: "+strings.Join(stillFailed, " "))
	fmt.Println(theme.Faint.Render("  имя пакета выведено из импорта и может отличаться на PyPI — поставьте руками"))
}

func needFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err != nil
}

// ensureFFmpeg ставит ffmpeg заранее: загрузчик Heroku отказывается ставить
// модули с аудио и видео (SpotifyMod и подобные) без него, причём отказ
// виден только в логе — в чате модуль просто не появляется.
func ensureFFmpeg() {
	if !needFFmpeg() {
		return
	}
	step("ffmpeg не найден — устанавливаю (без него не ставятся модули с аудио и видео)")
	ok := aptInstall("ffmpeg")
	result(ok, "ffmpeg установлен", "установка не удалась — см. вывод выше")
}
