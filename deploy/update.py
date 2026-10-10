#!/usr/bin/env python3
"""Служба обновления hrk-console: устанавливает выбранный релиз или возвращает резервную сборку.

Запускается от root отдельной службой hkc-update.service, поэтому переживает перезапуск
панели. Задание панель оставляет в файле запроса (каталог данных панели, рядом с
HKC_AUTH_FILE): {"action": "install", "version": "vX.Y.Z"} или {"action": "rollback",
"backup": "<имя>"}. Без файла устанавливается последний стабильный релиз.

Ход работы пишется в HKC_UPDATE_DIR/status.json, список резервных сборок — в
HKC_UPDATE_DIR/backups.json. Оба файла читаемы панелью (0644) и не содержат секретов;
сами резервные сборки лежат в закрытом каталоге backups/.
"""
import contextlib
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tarfile
import tempfile
import time
import urllib.request
from datetime import datetime, timezone

REPOSITORY = "https://github.com/ayanamisuicide/hrk-console"
TAG = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+$")
BACKUP_NAME = re.compile(r"^[0-9A-Za-z][0-9A-Za-z._-]{0,80}$")
MAX_ARCHIVE = 64 * 1024 * 1024
KEEP_BACKUPS = 5
# Имена машин из uname и соответствующие архитектуры релизных архивов.
ARCHITECTURES = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}


# Возвращает архитектуру релиза для текущей машины или объясняет, что сборки для неё нет.
def release_arch(machine=None):
    machine = machine or os.uname().machine
    if machine not in ARCHITECTURES:
        raise RuntimeError(f"нет сборки для архитектуры {machine}; поддерживаются Linux amd64 и arm64")
    return ARCHITECTURES[machine]


# Запускает команду с таймаутом без интерактивного Git; при ошибке возвращает ограниченное пояснение.
def run(args, timeout=90):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout,
                            env={**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0"})
    if result.returncode:
        raise RuntimeError(f"ошибка команды {args[0]}: {result.stderr.strip()[:500]}")
    return result.stdout.strip()


# Вызывает Git только с заданными аргументами, без хуков; копии на /mnt используют Windows-преобразование строк.
def git(path, *args):
    options = ["git", "-c", "core.hooksPath=/dev/null"]
    if str(path).startswith("/mnt/"):
        options += ["-c", "core.autocrlf=true"]
    return run(options + ["-C", str(path), *args])


# Требует ветку main и отсутствие изменённых и новых файлов. Правки пользователя не удаляются.
def require_clean(path):
    if git(path, "symbolic-ref", "--short", "HEAD") != "main":
        raise RuntimeError(f"{path}: перед обновлением выберите ветку main")
    if git(path, "status", "--porcelain", "--untracked-files=normal"):
        raise RuntimeError(f"{path}: обнаружены локальные изменения; обновление отменено для их сохранения")


# Получает стабильный тег по публичной переадресации GitHub и проверяет формат версии.
def stable_release():
    request = urllib.request.Request(REPOSITORY + "/releases/latest", method="HEAD")
    with urllib.request.urlopen(request, timeout=30) as response:
        url = response.url
    prefix = REPOSITORY + "/releases/tag/"
    tag = url[len(prefix):] if url.startswith(prefix) else ""
    if not TAG.fullmatch(tag):
        raise RuntimeError("GitHub не вернул стабильный релиз")
    return tag


# Скачивает ограниченный по размеру ресурс по HTTPS частями; progress(получено, всего) вызывается по ходу.
def download(url, limit, progress=None):
    with urllib.request.urlopen(url, timeout=60) as response:
        if not response.url.startswith("https://"):
            raise RuntimeError("небезопасный протокол загрузки релиза")
        total = int(response.headers.get("Content-Length") or 0)
        chunks = []
        received = 0
        while True:
            chunk = response.read(256 * 1024)
            if not chunk:
                break
            received += len(chunk)
            if received > limit:
                raise RuntimeError("файл релиза превышает ограничение размера")
            chunks.append(chunk)
            if progress:
                progress(received, total)
    return b"".join(chunks)


# Сверяет имя и SHA-256, допускает один обычный файл hkc-web и сигнатуру ELF. Ничего не распаковывает в произвольные пути.
def verify_archive(data, checksum, name):
    fields = checksum.decode("ascii").strip().split()
    if len(fields) != 2 or fields[1].lstrip("*") != name or not re.fullmatch(r"[a-fA-F0-9]{64}", fields[0]):
        raise RuntimeError("некорректный формат контрольной суммы релиза")
    if hashlib.sha256(data).hexdigest().lower() != fields[0].lower():
        raise RuntimeError("контрольная сумма SHA-256 релиза не совпадает")
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        members = archive.getmembers()
        if len(members) != 1 or members[0].name not in ("hkc-web", "./hkc-web"):
            raise RuntimeError("неожиданное содержимое архива релиза")
        member = members[0]
        if not member.isfile() or not 0 < member.size <= MAX_ARCHIVE:
            raise RuntimeError("небезопасный файл бинарника в архиве")
        binary = archive.extractfile(member).read(MAX_ARCHIVE + 1)
    if not binary.startswith(b"\x7fELF") or len(binary) > MAX_ARCHIVE:
        raise RuntimeError("релиз не содержит исполняемый файл Linux")
    return binary


# Записывает исполняемый временный файл рядом с целью, синхронизирует и атомарно заменяет цель.
def replace_binary(path, data):
    fd, temporary = tempfile.mkstemp(prefix=".hkc-stage-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as handle:
            handle.write(data)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o755)
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


# Ждёт именно ожидаемый коммит чистой сборки через /api/version, а не просто открытый TCP-порт.
def await_health(url, expected, attempts=30):
    for _ in range(attempts):
        try:
            with urllib.request.urlopen(url + "/api/version", timeout=2) as response:
                installed = json.load(response)
            if installed.get("commit") == expected and not installed.get("modified"):
                return
        except (OSError, ValueError):
            pass
        time.sleep(1)
    raise RuntimeError("новый сервер не прошёл проверку версии и доступности")


# Пишет JSON атомарно с правами 0644: панель работает от другого пользователя и только читает.
def write_public_json(path, content):
    fd, name = tempfile.mkstemp(prefix=".status-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as handle:
            json.dump(content, handle, ensure_ascii=False)
        os.chmod(name, 0o644)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


# Сохраняет фазу, этап и последние 80 событий; reset начинает новую историю.
# step — машинный этап для интерфейса: prepare, download, verify, backup, switch, health, sources, done.
def save_status(directory, phase, message, **extra):
    now = datetime.now(timezone.utc).isoformat()
    previous = {}
    reset = extra.pop("reset", False)
    if not reset:
        with contextlib.suppress(OSError, ValueError):
            previous = json.loads((directory / "status.json").read_text(encoding="utf-8"))
    events = previous.get("events", [])
    progress = extra.pop("progress", previous.get("progress", 0))
    step = extra.pop("step", previous.get("step", ""))
    quiet = extra.pop("quiet", False)
    if not quiet:
        events.append({"at": now, "phase": phase, "message": message, "progress": progress, "step": step})
    content = {"phase": phase, "message": message, "progress": progress, "step": step,
               "startedAt": previous.get("startedAt", now), "updatedAt": now,
               "events": events[-80:], **{key: value for key, value in previous.items()
                                         if key in ("version", "backup", "action", "fromVersion", "warnings")}, **extra}
    write_public_json(directory / "status.json", content)


# Каталог состояния открыт на чтение (статус для панели), резервные сборки — только root.
def prepare_state(state):
    state.mkdir(parents=True, exist_ok=True)
    os.chmod(state, 0o755)
    backups = state / "backups"
    backups.mkdir(mode=0o700, exist_ok=True)
    os.chmod(backups, 0o700)
    return backups


# Перечисляет резервные сборки, записывает их публичный индекс и удаляет лишние старые.
def index_backups(state, keep=KEEP_BACKUPS):
    backups = state / "backups"
    items = []
    if backups.is_dir():
        for entry in sorted(backups.iterdir(), key=lambda path: path.name, reverse=True):
            binary = entry / "hkc-web"
            if not entry.is_dir() or entry.is_symlink() or not binary.is_file() or not BACKUP_NAME.fullmatch(entry.name):
                continue
            meta = {}
            with contextlib.suppress(OSError, ValueError):
                meta = json.loads((entry / "version.json").read_text(encoding="utf-8"))
            items.append({"name": entry.name, "version": meta.get("version", ""), "commit": meta.get("commit", ""),
                          "createdAt": datetime.fromtimestamp(binary.stat().st_mtime, timezone.utc).isoformat(),
                          "size": binary.stat().st_size})
    for stale in items[keep:]:
        shutil.rmtree(backups / stale["name"], ignore_errors=True)
    items = items[:keep]
    write_public_json(state / "backups.json", {"backups": items, "updatedAt": datetime.now(timezone.utc).isoformat()})
    return items


# Читает задание панели. Файл лежит в каталоге, доступном пользователю панели, поэтому
# открывается без перехода по ссылкам, ограничен по размеру и проверяется по строгой схеме.
def read_request(path, state):
    if not path:
        return {"action": "install"}
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except FileNotFoundError:
        return {"action": "install"}
    except OSError as error:
        raise RuntimeError(f"файл задания недоступен: {error.strerror}")
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode) or info.st_size > 4096:
            raise RuntimeError("файл задания имеет неверный тип или размер")
        with os.fdopen(fd, "rb") as handle:
            fd = None
            raw = handle.read(4097)
    finally:
        if fd is not None:
            os.close(fd)
    with contextlib.suppress(OSError):
        os.unlink(path)
    try:
        request = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        raise RuntimeError("файл задания повреждён")
    if not isinstance(request, dict) or set(request) - {"action", "version", "backup", "requestedAt"}:
        raise RuntimeError("файл задания содержит неизвестные поля")
    action = request.get("action")
    if action == "install":
        version = request.get("version") or ""
        if version and not TAG.fullmatch(version):
            raise RuntimeError("неверный формат версии в задании")
        return {"action": "install", "version": version}
    if action == "rollback":
        name = request.get("backup") or ""
        available = {item["name"] for item in index_backups(state)}
        if not BACKUP_NAME.fullmatch(name) or name not in available:
            raise RuntimeError("выбранная резервная сборка не найдена")
        return {"action": "rollback", "backup": name}
    raise RuntimeError("неизвестное действие в задании")


# Каталог установки, из которого systemd запускает панель. Переменная HKC_SOURCE_DIR
# могла остаться от прежней установки в другом каталоге: тогда служба подменила бы
# не тот бинарник, перезапуск поднял бы старую сборку и проверка версии провалилась бы.
def source_dir():
    configured = Path(os.environ["HKC_SOURCE_DIR"]).resolve()
    with contextlib.suppress(Exception):
        line = run(["systemctl", "show", "hkc-web.service", "--property=ExecStart", "--value"], timeout=10)
        match = re.search(r"path=(\S+)", line or "")
        if match:
            executable = Path(match.group(1))
            if executable.name == "hkc-web" and executable.parent.name == "bin" and executable.is_file():
                return executable.parent.parent.resolve()
    return configured


def request_path():
    if os.environ.get("HKC_UPDATE_REQUEST_FILE"):
        return os.environ["HKC_UPDATE_REQUEST_FILE"]
    if os.environ.get("HKC_AUTH_FILE"):
        return str(Path(os.environ["HKC_AUTH_FILE"]).parent / "update-request.json")
    return ""


# Сохраняет текущую сборку в резервную копию с её метаданными.
def make_backup(state, executable, current):
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S")
    label = current.get("version") if TAG.fullmatch(current.get("version", "")) else current.get("commit", "")[:12] or "unknown"
    backup = state / "backups" / f"{stamp}-{label}"
    backup.mkdir(mode=0o700, parents=True)
    shutil.copy2(executable, backup / "hkc-web")
    (backup / "version.json").write_text(json.dumps(current), encoding="utf-8")
    return backup


# Продвигает копии исходников до коммита только fast-forward. Основная копия обязательна
# (из неё запускается сама служба обновления), дополнительная — по возможности: её состояние
# не блокирует установку, а пропуск попадает в предупреждения.
def sync_sources(state, source, local, commit, version):
    warnings = []
    for copy, required in [(source, True), (local, False)]:
        if not copy:
            continue
        try:
            require_clean(copy)
            git(copy, "merge-base", "--is-ancestor", "HEAD", commit)
            git(copy, "merge", "--ff-only", commit)
        except Exception as error:
            if required:
                warnings.append(f"Исходники {copy} не переведены на {version}: {error}")
            else:
                warnings.append(f"Дополнительная копия {copy} пропущена: {error}")
    return warnings


def fetch_refs(copies, tag):
    for copy in copies:
        # Получаем только доверенный репозиторий, без пользовательских URL и команд.
        git(copy, "fetch", "--no-tags", REPOSITORY + ".git",
            "refs/heads/main:refs/remotes/origin/main", f"refs/tags/{tag}:refs/tags/{tag}")


# Устанавливает релиз: без version — последний стабильный. Исходники продвигаются лишь после
# проверки здоровья; ошибка запуска возвращает предыдущую сборку.
def install(version=""):
    source = source_dir()
    local = Path(os.environ["HKC_LOCAL_SOURCE_DIR"]).resolve() if os.environ.get("HKC_LOCAL_SOURCE_DIR") else None
    if local == source:
        local = None
    state = Path(os.environ["HKC_UPDATE_DIR"]).resolve()
    prepare_state(state)
    executable = source / "bin" / "hkc-web"
    health_url = os.environ.get("HKC_UPDATE_HEALTH_URL", "http://127.0.0.1:8080")
    switched = False
    backup = None
    old_commit = ""
    try:
        save_status(state, "checking", "Готовимся к установке: проверяем окружение.", progress=3, step="prepare",
                    reset=True, action="install", warnings=[])
        arch = release_arch()
        if executable.is_symlink():
            raise RuntimeError("замена исполняемого файла по символической ссылке запрещена")
        # Из основной копии запускается сама служба обновления: её правки сохраняем и не трогаем.
        require_clean(source)
        tag = version or stable_release()
        current = json.loads(run([str(executable), "--version-json"], timeout=10))
        old_commit = current.get("commit", "")
        save_status(state, "checking", f"Выбран релиз {tag}; установлена {current.get('version', '?')}.",
                    progress=8, version=tag, fromVersion=current.get("version", ""))
        fetch_refs([source], tag)
        commit = git(source, "rev-parse", f"{tag}^{{commit}}")
        if current.get("modified"):
            raise RuntimeError("установленный бинарник собран с локальными изменениями")
        if old_commit == commit:
            warnings = sync_sources(state, source, local, commit, tag)
            save_status(state, "complete", f"Версия {tag} уже установлена.", progress=100, step="done",
                        version=tag, warnings=warnings)
            return

        name = f"hkc-web-{tag}-linux-{arch}.tar.gz"
        base = f"{REPOSITORY}/releases/download/{tag}/"
        save_status(state, "downloading", f"Скачиваем {name}.", progress=12, step="download")
        last = [0.0]

        def report(received, total):
            if time.monotonic() - last[0] < 0.4 and received != total:
                return
            last[0] = time.monotonic()
            share = received / total if total else 0
            save_status(state, "downloading", "Скачиваем архив релиза.", progress=12 + int(share * 36),
                        downloaded=received, total=total, quiet=True)

        data = download(base + name, MAX_ARCHIVE, report)
        checksum = download(base + name + ".sha256", 1024)
        save_status(state, "checking", "Проверяем подлинность: SHA-256, состав архива и версию.", progress=50, step="verify")
        binary = verify_archive(data, checksum, name)
        # Проверяем встроенную ревизию до замены установленного бинарника.
        with tempfile.TemporaryDirectory(prefix="hkc-verify-") as temporary:
            staged = Path(temporary) / "hkc-web"
            staged.write_bytes(binary)
            staged.chmod(0o755)
            metadata = json.loads(run([str(staged), "--version-json"], timeout=10))
            if metadata.get("commit") != commit or metadata.get("version") != tag or metadata.get("modified"):
                raise RuntimeError("метаданные бинарника не совпадают с опубликованным тегом Git")
        save_status(state, "checking", "Сборка подлинная: контрольная сумма и версия совпадают с тегом.", progress=58)

        save_status(state, "restarting", "Сохраняем текущую сборку для отката.", progress=64, step="backup")
        backup = make_backup(state, executable, current)
        index_backups(state)
        save_status(state, "restarting", "Устанавливаем новую сборку и перезапускаем панель.", progress=72,
                    step="switch", backup=str(backup))
        replace_binary(executable, binary)
        switched = True
        run(["systemctl", "restart", "hkc-web.service"], timeout=40)
        save_status(state, "restarting", "Ждём ответа новой сборки.", progress=82, step="health")
        await_health(health_url, commit)
        save_status(state, "restarting", "Новая сборка работает; обновляем исходники.", progress=92, step="sources")
        warnings = sync_sources(state, source, local, commit, tag)
        save_status(state, "complete", f"Готово: установлена {tag}.", progress=100, step="done",
                    version=tag, backup=str(backup), warnings=warnings)
    except Exception as error:
        if switched and backup:
            try:
                replace_binary(executable, (backup / "hkc-web").read_bytes())
                run(["systemctl", "restart", "hkc-web.service"], timeout=40)
                await_health(health_url, old_commit)
                save_status(state, "rolled_back", f"Новая сборка не запустилась, вернули предыдущую: {error}",
                            progress=100, step="done", backup=str(backup))
            except Exception as rollback_error:
                save_status(state, "failed", f"Обновление: {error}; откат: {rollback_error}", progress=100,
                            step="done", backup=str(backup))
        else:
            save_status(state, "failed", str(error), progress=100, step="done")
        raise
    finally:
        with contextlib.suppress(Exception):
            index_backups(state)


# Возвращает резервную сборку. Текущая перед этим тоже сохраняется, поэтому откат обратим.
# Исходники назад не переводятся: git продвигается только вперёд, расхождение — предупреждение.
def rollback(name):
    source = source_dir()
    state = Path(os.environ["HKC_UPDATE_DIR"]).resolve()
    prepare_state(state)
    executable = source / "bin" / "hkc-web"
    health_url = os.environ.get("HKC_UPDATE_HEALTH_URL", "http://127.0.0.1:8080")
    target = state / "backups" / name
    save_status(state, "checking", f"Готовим откат к резервной сборке {name}.", progress=5, step="prepare",
                reset=True, action="rollback", warnings=[])
    current_backup = None
    switched = False
    current = {}
    try:
        if executable.is_symlink():
            raise RuntimeError("замена исполняемого файла по символической ссылке запрещена")
        binary = (target / "hkc-web").read_bytes()
        if not binary.startswith(b"\x7fELF"):
            raise RuntimeError("резервная сборка повреждена")
        with tempfile.TemporaryDirectory(prefix="hkc-verify-") as temporary:
            staged = Path(temporary) / "hkc-web"
            staged.write_bytes(binary)
            staged.chmod(0o755)
            metadata = json.loads(run([str(staged), "--version-json"], timeout=10))
        current = json.loads(run([str(executable), "--version-json"], timeout=10))
        save_status(state, "checking", f"Резервная сборка {metadata.get('version') or name} проверена.", progress=30,
                    step="verify", version=metadata.get("version", ""), fromVersion=current.get("version", ""))
        save_status(state, "restarting", "Сохраняем текущую сборку, чтобы откат можно было отменить.", progress=45, step="backup")
        current_backup = make_backup(state, executable, current)
        index_backups(state)
        save_status(state, "restarting", "Возвращаем резервную сборку и перезапускаем панель.", progress=65,
                    step="switch", backup=str(current_backup))
        replace_binary(executable, binary)
        switched = True
        run(["systemctl", "restart", "hkc-web.service"], timeout=40)
        save_status(state, "restarting", "Ждём ответа панели.", progress=85, step="health")
        await_health(health_url, metadata.get("commit", ""))
        warnings = []
        if metadata.get("commit") and git(source, "rev-parse", "HEAD") != metadata.get("commit"):
            warnings.append("Исходники остались на более новой версии: следующая установка выровняет их.")
        save_status(state, "complete", f"Откат выполнен: работает {metadata.get('version') or name}.", progress=100,
                    step="done", warnings=warnings)
    except Exception as error:
        if switched and current_backup:
            with contextlib.suppress(Exception):
                replace_binary(executable, (current_backup / "hkc-web").read_bytes())
                run(["systemctl", "restart", "hkc-web.service"], timeout=40)
                await_health(health_url, current.get("commit", ""))
            save_status(state, "rolled_back", f"Резервная сборка не запустилась, вернули текущую: {error}",
                        progress=100, step="done")
        else:
            save_status(state, "failed", str(error), progress=100, step="done")
        raise
    finally:
        with contextlib.suppress(Exception):
            index_backups(state)


# Под внешней блокировкой читает задание и выполняет его. Блокировка в каталоге состояния
# root переживает перезапуск панели и не даёт запустить две установки сразу.
def main():
    state = Path(os.environ["HKC_UPDATE_DIR"]).resolve()
    prepare_state(state)
    with open(state / "lock", "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        try:
            request = read_request(request_path(), state)
        except Exception as error:
            save_status(state, "failed", str(error), progress=100, step="done", reset=True)
            raise
        if request["action"] == "rollback":
            rollback(request["backup"])
        else:
            install(request.get("version", ""))


if __name__ == "__main__":
    main()
