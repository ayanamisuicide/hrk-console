#!/usr/bin/env python3
"""One-time migration of the existing WSL panel to supervised updates."""
import argparse
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time
import urllib.request
from datetime import datetime, timezone
import update

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--check-only", action="store_true")
    args = parser.parse_args()
    source = Path("/root/heroku-console")
    local = Path("/mnt/c/Users/ayanami/Documents/ChatGPT/пупупу")
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
        raise RuntimeError(f"Expected one existing WSL panel, found {len(processes)}")
    pid = processes[0]
    raw_env = Path(f"/proc/{pid}/environ").read_bytes()
    original = dict(entry.decode().split("=", 1) for entry in raw_env.split(b"\0") if entry)
    token = original.get("HKC_ADMIN_TOKEN") or Path("/root/.config/hkc/admin.token").read_text().strip()
    # Confirm credentials locally without emitting them into logs.
    req = urllib.request.Request("http://127.0.0.1:8080/api/admin/overview",
                                 headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=5) as response:
        if response.status != 200:
            raise RuntimeError("Existing administrative credential did not authenticate")
    for copy in (source, local):
        update.require_clean(copy)
    metadata = json.loads(update.run([str(args.binary.resolve()), "--version-json"]))
    if metadata.get("modified") or not update.TAG.fullmatch(metadata.get("version", "")):
        raise RuntimeError("Bootstrap binary must be a clean release")
    for copy in (source, local):
        update.git(copy, "fetch", "--no-tags", update.REPOSITORY + ".git",
                   "refs/heads/main:refs/remotes/origin/main",
                   f"refs/tags/{metadata['version']}:refs/tags/{metadata['version']}")
        if update.git(copy, "rev-parse", metadata["version"] + "^{commit}") != metadata["commit"]:
            raise RuntimeError("Bootstrap release tag mismatch")
        update.git(copy, "merge-base", "--is-ancestor", "HEAD", metadata["commit"])
    if args.check_only:
        print("Existing credentials and both source copies verified; migration is ready.")
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
                "HKC_LOCAL_SOURCE_DIR": str(local), "HKC_UPDATE_DIR": str(state),
                "HKC_UPDATE_ENABLED": "1", "HKC_WEB_ADDR": "127.0.0.1:8080",
                "HKC_UPDATE_HEALTH_URL": "http://127.0.0.1:8080"})
    env.setdefault("HKC_AUTH_FILE", "/root/.config/hkc/web-auth.json")
    envfile = config / "hkc.env"
    if envfile.exists():
        shutil.copy2(envfile, backup / "hkc.env")
    descriptor = os.open(envfile, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(descriptor, "w") as handle:
        for key, value in env.items():
            handle.write(key + "=" + json.dumps(value, ensure_ascii=False) + "\n")
    scripts = Path(__file__).resolve().parent
    helper = Path("/opt/hkc-updater")
    helper.mkdir(mode=0o700, exist_ok=True)
    shutil.copy2(scripts / "update.py", helper / "update.py")
    for name in ("hkc-web.service", "hkc-update.service"):
        destination = Path("/etc/systemd/system") / name
        if destination.exists():
            shutil.copy2(destination, backup / name)
        shutil.copy2(scripts / name, destination)
    update.run(["systemctl", "daemon-reload"])
    update.replace_binary(executable, args.binary.read_bytes())
    try:
        # Validate exact process identity again before stopping the old panel.
        if Path(f"/proc/{pid}/exe").resolve().as_posix().removesuffix(" (deleted)") != str(executable):
            raise RuntimeError("Panel process identity changed")
        os.kill(pid, signal.SIGTERM)
        for _ in range(50):
            if not Path(f"/proc/{pid}").exists():
                break
            time.sleep(.1)
        update.run(["systemctl", "start", "hkc-web.service"])
        update.await_health("http://127.0.0.1:8080", metadata["commit"])
        for copy in (source, local):
            update.require_clean(copy)
            update.git(copy, "merge", "--ff-only", metadata["commit"])
        update.run(["systemctl", "enable", "hkc-web.service"])
        update.save_status(state, "complete", "Служба обновлений подключена; предыдущая сборка сохранена.",
                           version=metadata["version"], backup=str(backup))
        print("WSL panel upgraded, health verified, local copies synchronized; previous binary:", backup)
    except Exception:
        update.replace_binary(executable, (backup / "hkc-web").read_bytes())
        update.run(["systemctl", "restart", "hkc-web.service"])
        update.save_status(state, "rolled_back", "Начальная установка не прошла; возвращена предыдущая сборка.", backup=str(backup))
        raise

if __name__ == "__main__":
    main()
