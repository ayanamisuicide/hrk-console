#!/usr/bin/env python3
"""Устанавливает релиз hrk-console, сохраняя старую сборку и проверяя новую службу."""
import contextlib
import fcntl
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.request
from datetime import datetime, timezone

REPOSITORY = "https://github.com/ayanamisuicide/hrk-console"
TAG = re.compile(r"^v[0-9]+\.[0-9]+\.[0-9]+$")
MAX_ARCHIVE = 64 * 1024 * 1024


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


# Скачивает ограниченный по размеру ресурс по HTTPS, читая лишний байт для обнаружения превышения лимита.
def download(url, limit):
    with urllib.request.urlopen(url, timeout=60) as response:
        if not response.url.startswith("https://"):
            raise RuntimeError("небезопасный протокол загрузки релиза")
        data = response.read(limit + 1)
    if len(data) > limit:
        raise RuntimeError("файл релиза превышает ограничение размера")
    return data


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


# Сохраняет фазу и последние 80 событий через временный JSON; reset начинает новую историю установки.
def save_status(directory, phase, message, **extra):
    now = datetime.now(timezone.utc).isoformat()
    previous = {}
    reset = extra.pop("reset", False)
    if not reset:
        with contextlib.suppress(OSError, ValueError):
            previous = json.loads((directory / "status.json").read_text(encoding="utf-8"))
    events = previous.get("events", [])
    progress = extra.pop("progress", previous.get("progress", 0))
    events.append({"at": now, "phase": phase, "message": message, "progress": progress})
    content = {"phase": phase, "message": message, "progress": progress,
               "startedAt": previous.get("startedAt", now), "updatedAt": now,
               "events": events[-80:], **{key: value for key, value in previous.items()
                                         if key in ("version", "backup")}, **extra}
    fd, name = tempfile.mkstemp(prefix=".status-", dir=directory)
    try:
        with os.fdopen(fd, "w") as handle:
            json.dump(content, handle, ensure_ascii=False)
        os.replace(name, directory / "status.json")
    finally:
        if os.path.exists(name):
            os.unlink(name)


# Под внешней блокировкой проверяет релиз, сохраняет старый бинарник и перезапускает службу. Исходники продвигаются лишь после проверки здоровья; ошибка запуска вызывает откат.
def install():
    source = Path(os.environ["HKC_SOURCE_DIR"]).resolve()
    local = Path(os.environ["HKC_LOCAL_SOURCE_DIR"]).resolve() if os.environ.get("HKC_LOCAL_SOURCE_DIR") else None
    state = Path(os.environ["HKC_UPDATE_DIR"]).resolve()
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    # Блокировка в каталоге состояния, принадлежащем root, переживает перезапуск панели.
    with open(state / "lock", "a") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            return
        switched = False
        backup = None
        executable = source / "bin" / "hkc-web"
        old_commit = ""
        try:
            save_status(state, "checking", "Запуск установки: проверяем окружение.", progress=3, reset=True)
            if os.uname().machine != "x86_64":
                raise RuntimeError("пока доступны только сборки Linux amd64")
            if executable.is_symlink():
                raise RuntimeError("замена исполняемого файла по символической ссылке запрещена")
            copies = [source] + ([local] if local and local != source else [])
            for copy in copies:
                require_clean(copy)
            save_status(state, "checking", "Исходники чистые и находятся на main.", progress=10)
            tag = stable_release()
            save_status(state, "checking", f"Найден стабильный релиз {tag}.", progress=16, version=tag)
            for copy in copies:
                # Получаем только доверенный репозиторий, без пользовательских URL и команд.
                git(copy, "fetch", "--no-tags", REPOSITORY + ".git",
                    "refs/heads/main:refs/remotes/origin/main", f"refs/tags/{tag}:refs/tags/{tag}")
            commit = git(source, "rev-parse", f"{tag}^{{commit}}")
            save_status(state, "checking", f"Сверены GitHub refs и коммит {commit[:12]}.", progress=24)
            for copy in copies:
                git(copy, "merge-base", "--is-ancestor", "HEAD", commit)
            current = json.loads(run([str(executable), "--version-json"], timeout=10))
            old_commit = current.get("commit", "")
            if current.get("modified"):
                raise RuntimeError("установленный бинарник собран с локальными изменениями")
            if old_commit == commit:
                save_status(state, "checking", "Сборка уже актуальна; сверяем копии исходников.", progress=82)
                for copy in copies:
                    require_clean(copy)
                    git(copy, "merge", "--ff-only", commit)
                save_status(state, "complete", "Установлена актуальная версия. Локальные копии сверены.", progress=100, version=tag)
                return

            save_status(state, "downloading", "Загружаем архив релиза и контрольную сумму.", progress=32, version=tag)
            name = f"hkc-web-{tag}-linux-amd64.tar.gz"
            base = f"{REPOSITORY}/releases/download/{tag}/"
            binary = verify_archive(download(base + name, MAX_ARCHIVE),
                                    download(base + name + ".sha256", 1024), name)
            save_status(state, "downloading", "Архив скачан; SHA-256 и содержимое подтверждены.", progress=48)
            # Проверяем встроенную ревизию до замены установленного бинарника.
            with tempfile.TemporaryDirectory(prefix="hkc-verify-") as temporary:
                staged = Path(temporary) / "hkc-web"
                staged.write_bytes(binary)
                staged.chmod(0o755)
                metadata = json.loads(run([str(staged), "--version-json"], timeout=10))
                if metadata.get("commit") != commit or metadata.get("version") != tag or metadata.get("modified"):
                    raise RuntimeError("метаданные бинарника не совпадают с опубликованным тегом Git")
            save_status(state, "checking", "Встроенная версия бинарника совпадает с тегом и коммитом.", progress=58)

            for copy in copies:
                require_clean(copy)
                git(copy, "merge-base", "--is-ancestor", "HEAD", commit)
            stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S")
            backup = state / "backups" / f"{stamp}-{old_commit[:12]}"
            backup.mkdir(mode=0o700, parents=True)
            shutil.copy2(executable, backup / "hkc-web")
            (backup / "version.json").write_text(json.dumps(current), encoding="utf-8")
            save_status(state, "restarting", "Предыдущая сборка сохранена для отката.", progress=68, version=tag, backup=str(backup))
            replace_binary(executable, binary)
            switched = True
            save_status(state, "restarting", "Новый бинарник установлен; перезапускаем службу.", progress=74)
            run(["systemctl", "restart", "hkc-web.service"], timeout=40)
            health_url = os.environ.get("HKC_UPDATE_HEALTH_URL", "http://127.0.0.1:8080")
            save_status(state, "restarting", "Служба поднята; проверяем HTTP и номер коммита.", progress=82)
            await_health(health_url, commit)
            save_status(state, "restarting", "Новая сборка отвечает корректно; синхронизируем исходники.", progress=90)
            # Продвигаем исходники только после проверки работающей новой сборки.
            # Перед каждым слиянием снова проверяем чистоту: правки пользователя не сбрасываем.
            for copy in copies:
                require_clean(copy)
                git(copy, "merge", "--ff-only", commit)
            save_status(state, "complete", "Обновление установлено; сборка и локальные копии совпадают с релизом.", progress=100, version=tag, backup=str(backup))
        except Exception as error:
            if switched and backup:
                try:
                    replace_binary(executable, (backup / "hkc-web").read_bytes())
                    run(["systemctl", "restart", "hkc-web.service"], timeout=40)
                    await_health(os.environ.get("HKC_UPDATE_HEALTH_URL", "http://127.0.0.1:8080"), old_commit)
                    save_status(state, "rolled_back", f"Возвращена предыдущая сборка: {error}", progress=100, backup=str(backup))
                except Exception as rollback_error:
                    save_status(state, "failed", f"Обновление: {error}; откат: {rollback_error}", progress=100, backup=str(backup))
            else:
                save_status(state, "failed", str(error), progress=100)
            raise


if __name__ == "__main__":
    install()
