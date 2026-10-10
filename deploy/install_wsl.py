#!/usr/bin/env python3
"""Переводит существующую WSL-панель на обновления под управлением systemd."""
import argparse
import json
import os
from pathlib import Path
import pwd
import shutil
import signal
import subprocess
import time
import urllib.request
from datetime import datetime, timezone
import update

# Проверяет существующий процесс и релиз, сохраняет настройки и подключает службы systemd. Ошибка проверки новой службы возвращает старый бинарник.
def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--source", type=Path,
                        default=Path(os.environ.get("HKC_SOURCE_DIR", "/root/heroku-console")))
    parser.add_argument("--local-source", type=Path,
                        default=Path(os.environ["HKC_LOCAL_SOURCE_DIR"])
                        if os.environ.get("HKC_LOCAL_SOURCE_DIR") else None)
    parser.add_argument("--service-user", default=os.environ.get("HKC_SERVICE_USER"),
                        help="учётная запись hkc-web.service (по умолчанию: владелец --source)")
    parser.add_argument("--check-only", action="store_true")
    args = parser.parse_args()
    source = args.source.expanduser().resolve()
    local = args.local_source.expanduser().resolve() if args.local_source else None
    copies = [source] + ([local] if local and local != source else [])
    args.service_user = args.service_user or pwd.getpwuid(source.stat().st_uid).pw_name
    if not args.service_user or any(char.isspace() for char in args.service_user):
        raise RuntimeError("некорректное имя пользователя службы")
    executable = source / "bin/hkc-web"
    processes = []
    for item in Path("/proc").iterdir():
        if item.name.isdigit():
            try:
                if (item / "exe").resolve() == executable and (item / "cwd").resolve() == source:
                    processes.append(int(item.name))
            except OSError:
                pass
    if len(processes) != 1:
        raise RuntimeError(f"ожидался один процесс WSL-панели, найдено: {len(processes)}")
    pid = processes[0]
    raw_env = Path(f"/proc/{pid}/environ").read_bytes()
    original = dict(entry.decode().split("=", 1) for entry in raw_env.split(b"\0") if entry)
    token = original.get("HKC_ADMIN_TOKEN") or Path("/root/.config/hkc/admin.token").read_text().strip()
    # Проверяем административный доступ локально, не выводя секрет в журнал.
    req = urllib.request.Request("http://127.0.0.1:8080/api/admin/overview",
                                 headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=5) as response:
        if response.status != 200:
            raise RuntimeError("существующий административный токен не прошёл проверку")
    for copy in copies:
        update.require_clean(copy)
    metadata = json.loads(update.run([str(args.binary.resolve()), "--version-json"]))
    if metadata.get("modified") or not update.TAG.fullmatch(metadata.get("version", "")):
        raise RuntimeError("бинарник начальной установки должен быть чистой релизной сборкой")
    for copy in copies:
        update.git(copy, "fetch", "--no-tags", update.REPOSITORY + ".git",
                   "refs/heads/main:refs/remotes/origin/main",
                   f"refs/tags/{metadata['version']}:refs/tags/{metadata['version']}")
        if update.git(copy, "rev-parse", metadata["version"] + "^{commit}") != metadata["commit"]:
            raise RuntimeError("тег начальной сборки не совпадает с релизом")
        update.git(copy, "merge-base", "--is-ancestor", "HEAD", metadata["commit"])
    if args.check_only:
        print("доступ и настроенные копии исходников проверены; можно подключать службы.")
        return
    state = Path("/root/.config/hkc/updates")
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    backup = state / "backups" / ("bootstrap-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S"))
    backup.mkdir(mode=0o700, parents=True)
    shutil.copy2(executable, backup / "hkc-web")
    (backup / "source-commit.txt").write_text(update.git(source, "rev-parse", "HEAD"))
    config = Path("/etc/hkc")
    config.mkdir(mode=0o700, exist_ok=True)
    env = {key: value for key, value in original.items() if key.startswith("HKC_") or key == "HEROKU_DIR"}
    env.update({"HKC_ADMIN_TOKEN": token, "HKC_SOURCE_DIR": str(source),
                "HKC_UPDATE_DIR": str(state),
                "HKC_UPDATE_ENABLED": "1", "HKC_WEB_ADDR": "127.0.0.1:8080",
                "HKC_UPDATE_HEALTH_URL": "http://127.0.0.1:8080"})
    env["HKC_SERVICE_USER"] = args.service_user
    if local:
        env["HKC_LOCAL_SOURCE_DIR"] = str(local)
    else:
        env.pop("HKC_LOCAL_SOURCE_DIR", None)
    env.setdefault("HKC_AUTH_FILE", "/root/.config/hkc/web-auth.json")
    envfile = config / "hkc.env"
    if envfile.exists():
        shutil.copy2(envfile, backup / "hkc.env")
    descriptor = os.open(envfile, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as handle:
        for key, value in env.items():
            handle.write(key + "=" + json.dumps(value, ensure_ascii=False) + "\n")
    scripts = Path(__file__).resolve().parent
    for name in ("hkc-web.service", "hkc-update.service"):
        destination = Path("/etc/systemd/system") / name
        if destination.exists():
            shutil.copy2(destination, backup / name)
        content = (scripts / name).read_text(encoding="utf-8")
        content = content.replace("__HKC_SOURCE_DIR__", str(source))
        content = content.replace("__HKC_SERVICE_USER__", args.service_user)
        descriptor = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o644)
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(content)
    sudoers = Path("/etc/sudoers.d/hkc-update")
    if args.service_user != "root":
        rule = f"{args.service_user} ALL=(root) NOPASSWD: /usr/bin/systemctl start --no-block hkc-update.service\n"
        descriptor = os.open(sudoers, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o440)
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(rule)
        update.run(["visudo", "-cf", str(sudoers)])
    elif sudoers.exists():
        sudoers.unlink()
    update.run(["systemctl", "daemon-reload"])
    update.replace_binary(executable, args.binary.read_bytes())
    try:
        # Перед остановкой повторно сверяем исполняемый файл именно старой панели.
        if Path(f"/proc/{pid}/exe").resolve().as_posix().removesuffix(" (deleted)") != str(executable):
            raise RuntimeError("исполняемый файл процесса панели изменился")
        managed_pid = update.run(["systemctl", "show", "hkc-web.service", "--property=MainPID", "--value"])
        if managed_pid == str(pid):
            update.run(["systemctl", "stop", "hkc-web.service"])
        else:
            os.kill(pid, signal.SIGTERM)
        for _ in range(50):
            if not Path(f"/proc/{pid}").exists():
                break
            time.sleep(.1)
        update.run(["systemctl", "start", "hkc-web.service"])
        update.await_health("http://127.0.0.1:8080", metadata["commit"])
        for copy in copies:
            update.require_clean(copy)
            update.git(copy, "merge", "--ff-only", metadata["commit"])
        update.run(["systemctl", "enable", "hkc-web.service"])
        update.save_status(state, "complete", "Служба обновлений подключена; предыдущая сборка сохранена.",
                           version=metadata["version"], backup=str(backup))
        print("WSL-панель обновлена и проверена, исходники синхронизированы; предыдущая сборка:", backup)
    except Exception as error:
        update.replace_binary(executable, (backup / "hkc-web").read_bytes())
        update.run(["systemctl", "restart", "hkc-web.service"])
        # У старых сборок нет /api/version: проверяем их основной HTTP-адрес.
        restored = False
        for _ in range(30):
            try:
                with urllib.request.urlopen("http://127.0.0.1:8080/", timeout=2) as response:
                    restored = response.status == 200
                if restored:
                    break
            except OSError:
                pass
            time.sleep(1)
        phase = "rolled_back" if restored else "failed"
        message = "Возвращена предыдущая сборка." if restored else "Предыдущая сборка восстановлена на диске, но сервер не отвечает."
        update.save_status(state, phase, f"{message} Причина: {error}", backup=str(backup))
        raise

if __name__ == "__main__":
    main()
