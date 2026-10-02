#!/usr/bin/env python3
"""Install a published hrk-console release, preserve the binary and verify health."""
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


def run(args, timeout=90):
    result = subprocess.run(args, capture_output=True, text=True, timeout=timeout,
                            env={**os.environ, "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0"})
    if result.returncode:
        raise RuntimeError(f"{args[0]} failed: {result.stderr.strip()[:500]}")
    return result.stdout.strip()


def git(path, *args):
    options = ["git", "-c", "core.hooksPath=/dev/null"]
    if str(path).startswith("/mnt/"):
        options += ["-c", "core.autocrlf=true"]
    return run(options + ["-C", str(path), *args])


def require_clean(path):
    if git(path, "symbolic-ref", "--short", "HEAD") != "main":
        raise RuntimeError(f"{path}: select main before updating")
    if git(path, "status", "--porcelain", "--untracked-files=normal"):
        raise RuntimeError(f"{path}: local changes must be preserved; update refused")


def stable_release():
    request = urllib.request.Request(REPOSITORY + "/releases/latest", method="HEAD")
    with urllib.request.urlopen(request, timeout=30) as response:
        url = response.url
    prefix = REPOSITORY + "/releases/tag/"
    tag = url[len(prefix):] if url.startswith(prefix) else ""
    if not TAG.fullmatch(tag):
        raise RuntimeError("GitHub did not return a stable release")
    return tag


def download(url, limit):
    with urllib.request.urlopen(url, timeout=60) as response:
        if not response.url.startswith("https://"):
            raise RuntimeError("Insecure release download")
        data = response.read(limit + 1)
    if len(data) > limit:
        raise RuntimeError("Release asset exceeds size limit")
    return data


def verify_archive(data, checksum, name):
    fields = checksum.decode("ascii").strip().split()
    if len(fields) != 2 or fields[1].lstrip("*") != name or not re.fullmatch(r"[a-fA-F0-9]{64}", fields[0]):
        raise RuntimeError("Malformed release checksum")
    if hashlib.sha256(data).hexdigest().lower() != fields[0].lower():
        raise RuntimeError("Release SHA-256 mismatch")
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        members = archive.getmembers()
        if len(members) != 1 or members[0].name not in ("hkc-web", "./hkc-web"):
            raise RuntimeError("Unexpected release archive contents")
        member = members[0]
        if not member.isfile() or not 0 < member.size <= MAX_ARCHIVE:
            raise RuntimeError("Unsafe release binary")
        binary = archive.extractfile(member).read(MAX_ARCHIVE + 1)
    if not binary.startswith(b"\x7fELF") or len(binary) > MAX_ARCHIVE:
        raise RuntimeError("Release is not a Linux executable")
    return binary


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
    raise RuntimeError("New server did not pass the version/health check")


def save_status(directory, phase, message, **extra):
    content = {"phase": phase, "message": message,
               "updatedAt": datetime.now(timezone.utc).isoformat(), **extra}
    fd, name = tempfile.mkstemp(prefix=".status-", dir=directory)
    try:
        with os.fdopen(fd, "w") as handle:
            json.dump(content, handle, ensure_ascii=False)
        os.replace(name, directory / "status.json")
    finally:
        if os.path.exists(name):
            os.unlink(name)


def install():
    source = Path(os.environ["HKC_SOURCE_DIR"]).resolve()
    local = Path(os.environ["HKC_LOCAL_SOURCE_DIR"]).resolve() if os.environ.get("HKC_LOCAL_SOURCE_DIR") else None
    state = Path(os.environ["HKC_UPDATE_DIR"]).resolve()
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    # A root-owned lock outside the source checkout survives panel restarts.
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
            save_status(state, "checking", "Проверяем релиз и локальные изменения.")
            if os.uname().machine != "x86_64":
                raise RuntimeError("Only Linux amd64 releases are currently available")
            if executable.is_symlink():
                raise RuntimeError("Refusing to replace a symlink executable")
            copies = [source] + ([local] if local and local != source else [])
            for copy in copies:
                require_clean(copy)
            tag = stable_release()
            for copy in copies:
                # Only the trusted project is fetched; no user-supplied URL or command.
                git(copy, "fetch", "--no-tags", REPOSITORY + ".git",
                    "refs/heads/main:refs/remotes/origin/main", f"refs/tags/{tag}:refs/tags/{tag}")
            commit = git(source, "rev-parse", f"{tag}^{{commit}}")
            for copy in copies:
                git(copy, "merge-base", "--is-ancestor", "HEAD", commit)
            current = json.loads(run([str(executable), "--version-json"], timeout=10))
            old_commit = current.get("commit", "")
            if current.get("modified"):
                raise RuntimeError("The installed binary was built with local changes")
            if old_commit == commit:
                for copy in copies:
                    require_clean(copy)
                    git(copy, "merge", "--ff-only", commit)
                save_status(state, "complete", "Установлена актуальная версия. Локальные копии сверены.", version=tag)
                return

            save_status(state, "downloading", "Скачиваем релиз и проверяем SHA-256.", version=tag)
            name = f"hkc-web-{tag}-linux-amd64.tar.gz"
            base = f"{REPOSITORY}/releases/download/{tag}/"
            binary = verify_archive(download(base + name, MAX_ARCHIVE),
                                    download(base + name + ".sha256", 1024), name)
            # Verify the embedded revision before touching the installed executable.
            with tempfile.TemporaryDirectory(prefix="hkc-verify-") as temporary:
                staged = Path(temporary) / "hkc-web"
                staged.write_bytes(binary)
                staged.chmod(0o755)
                metadata = json.loads(run([str(staged), "--version-json"], timeout=10))
                if metadata.get("commit") != commit or metadata.get("version") != tag or metadata.get("modified"):
                    raise RuntimeError("Binary metadata does not match the published Git tag")

            for copy in copies:
                require_clean(copy)
                git(copy, "merge-base", "--is-ancestor", "HEAD", commit)
            stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S")
            backup = state / "backups" / f"{stamp}-{old_commit[:12]}"
            backup.mkdir(mode=0o700, parents=True)
            shutil.copy2(executable, backup / "hkc-web")
            (backup / "version.json").write_text(json.dumps(current), encoding="utf-8")
            save_status(state, "restarting", "Предыдущая сборка сохранена. Перезапускаем панель.", version=tag, backup=str(backup))
            replace_binary(executable, binary)
            switched = True
            run(["systemctl", "restart", "hkc-web.service"], timeout=40)
            health_url = os.environ.get("HKC_UPDATE_HEALTH_URL", "http://127.0.0.1:8080")
            await_health(health_url, commit)
            # Source copies advance only after the new process is healthy.
            # Each is rechecked immediately before merge; user edits are never reset.
            for copy in copies:
                require_clean(copy)
                git(copy, "merge", "--ff-only", commit)
            save_status(state, "complete", "Обновление установлено; сборка и локальные копии совпадают с релизом.", version=tag, backup=str(backup))
        except Exception as error:
            if switched and backup:
                try:
                    replace_binary(executable, (backup / "hkc-web").read_bytes())
                    run(["systemctl", "restart", "hkc-web.service"], timeout=40)
                    await_health(os.environ.get("HKC_UPDATE_HEALTH_URL", "http://127.0.0.1:8080"), old_commit)
                    save_status(state, "rolled_back", f"Возвращена предыдущая сборка: {error}", backup=str(backup))
                except Exception as rollback_error:
                    save_status(state, "failed", f"Обновление: {error}; откат: {rollback_error}", backup=str(backup))
            else:
                save_status(state, "failed", str(error))
            raise


if __name__ == "__main__":
    install()
