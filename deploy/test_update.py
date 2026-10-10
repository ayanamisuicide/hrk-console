import hashlib
import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import update

# Тесты не должны трогать настоящий /etc/systemd даже при запуске от root.
_SYSTEMD_DIR = tempfile.TemporaryDirectory(prefix="hkc-systemd-")
update.KEEP_BOT_DROPIN = Path(_SYSTEMD_DIR.name) / "hkc-web.service.d" / "keep-bot.conf"


# Создаёт маленький архив в памяти для проверки допустимых и опасных вариантов релиза.
def archive(name="hkc-web", kind=tarfile.REGTYPE, payload=b"\x7fELFtest"):
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode="w:gz") as tar:
        item = tarfile.TarInfo(name)
        item.type = kind
        item.size = len(payload) if kind == tarfile.REGTYPE else 0
        tar.addfile(item, io.BytesIO(payload) if item.size else None)
    return data.getvalue()


class ReleaseTests(unittest.TestCase):
    # Проверяет ограниченную историю этапов и очистку при новой установке.
    def test_progress_status_retains_bounded_timeline(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            update.save_status(directory, "checking", "Начало", progress=3)
            for index in range(90):
                update.save_status(directory, "downloading", f"Шаг {index}", progress=40)
            status = json.loads((directory / "status.json").read_text())
            self.assertEqual(status["phase"], "downloading")
            self.assertEqual(status["progress"], 40)
            self.assertEqual(len(status["events"]), 80)
            self.assertEqual(status["events"][-1]["message"], "Шаг 89")
            update.save_status(directory, "checking", "Новая установка", progress=3, reset=True)
            status = json.loads((directory / "status.json").read_text())
            self.assertEqual(len(status["events"]), 1)

    # Создаёт правильную контрольную сумму тестового архива и вызывает настоящий проверяющий код.
    def verify(self, data):
        checksum = (hashlib.sha256(data).hexdigest() + "  release.tar.gz\n").encode()
        return update.verify_archive(data, checksum, "release.tar.gz")

    # Проверяет допустимый архив с ELF-сигнатурой.
    def test_valid_binary(self):
        self.assertEqual(self.verify(archive()), b"\x7fELFtest")

    # Проверяет отказ обхода каталогов, ссылок и файла без ELF-сигнатуры.
    def test_reject_paths_links_and_non_elf(self):
        for data in [archive("../hkc-web"), archive(kind=tarfile.SYMTYPE), archive(payload=b"not ELF")]:
            with self.subTest(data=data[:8]), self.assertRaises(RuntimeError):
                self.verify(data)

    # Проверяет выбор архива по архитектуре машины.
    def test_release_arch(self):
        self.assertEqual(update.release_arch("x86_64"), "amd64")
        self.assertEqual(update.release_arch("aarch64"), "arm64")
        with self.assertRaises(RuntimeError):
            update.release_arch("armv7l")

    # Проверяет отказ несовпадающей контрольной суммы.
    def test_reject_tampered_archive(self):
        with self.assertRaises(RuntimeError):
            update.verify_archive(archive(), b"0" * 64 + b"  release.tar.gz", "release.tar.gz")

    # Проверяет имя архива в файле контрольной суммы.
    def test_reject_wrong_checksum_filename(self):
        data = archive()
        with self.assertRaises(RuntimeError):
            update.verify_archive(data, (hashlib.sha256(data).hexdigest() + "  wrong.tar.gz").encode(), "release.tar.gz")

    # Имитирует неудачный запуск и проверяет восстановление бинарника без продвижения исходников.
    def test_health_failure_restores_previous_binary_before_source_merge(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "bin").mkdir()
            executable = root / "bin/hkc-web"
            executable.write_bytes(b"previous binary")
            old, new = "a" * 40, "b" * 40
            metadata = [{"version":"v2.1.0","commit":old}, {"version":"v2.2.0","commit":new}]
            calls = []

            # Подменяет команды и собирает вызовы без изменения настоящей службы.
            def fake_run(args, **kwargs):
                calls.append(args)
                if args[-1] == "--version-json":
                    return json.dumps(metadata.pop(0))
                return ""

            # Подменяет Git, возвращая ожидаемый коммит и записывая команды.
            def fake_git(path, *args):
                calls.append(list(args))
                return new if args[0] == "rev-parse" else ""

            with patch.dict(os.environ, {"HKC_SOURCE_DIR":str(root),"HKC_LOCAL_SOURCE_DIR":"","HKC_UPDATE_DIR":str(root / "state")}), \
                 patch.object(update, "require_clean"), patch.object(update, "stable_release", return_value="v2.2.0"), \
                 patch.object(update, "git", side_effect=fake_git), patch.object(update, "run", side_effect=fake_run), \
                 patch.object(update, "download", return_value=b"archive"), patch.object(update, "verify_archive", return_value=b"new binary"), \
                 patch.object(update, "await_health", side_effect=[RuntimeError("health failed"), None]):
                with self.assertRaises(RuntimeError):
                    update.install()
            self.assertEqual(executable.read_bytes(), b"previous binary")
            self.assertEqual(json.loads((root / "state/status.json").read_text())["phase"], "rolled_back")
            self.assertFalse(any(call[0] == "merge" for call in calls))
            self.assertEqual(sum(call == ["systemctl","restart","hkc-web.service"] for call in calls), 2)

    # Статус и индекс резервных копий читаемы панелью, сами копии закрыты, лишние удаляются.
    def test_backup_index_is_public_and_pruned(self):
        with tempfile.TemporaryDirectory() as temporary:
            state = Path(temporary) / "state"
            backups = update.prepare_state(state)
            for index in range(7):
                entry = backups / f"20261010T0000{index:02d}-v2.{index}.0"
                entry.mkdir()
                (entry / "hkc-web").write_bytes(b"\x7fELF" + bytes([index]))
                (entry / "version.json").write_text(json.dumps({"version": f"v2.{index}.0", "commit": "c" * 40}))
            (backups / "../evil").mkdir(exist_ok=True)
            items = update.index_backups(state)
            self.assertEqual([item["version"] for item in items], [f"v2.{index}.0" for index in range(6, 1, -1)])
            self.assertEqual(len(list(backups.iterdir())), 5)
            index = state / "backups.json"
            self.assertEqual(oct(index.stat().st_mode & 0o777), "0o644")
            self.assertEqual(oct(state.stat().st_mode & 0o777), "0o755")
            self.assertEqual(oct(backups.stat().st_mode & 0o777), "0o700")
            update.save_status(state, "checking", "проверка", step="prepare")
            self.assertEqual(oct((state / "status.json").stat().st_mode & 0o777), "0o644")

    # Задание панели: строгая схема, без ссылок, удаляется после чтения.
    def test_request_validation(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            state = root / "state"
            backups = update.prepare_state(state)
            (backups / "20261010T000000-v2.4.0").mkdir()
            (backups / "20261010T000000-v2.4.0" / "hkc-web").write_bytes(b"\x7fELF")
            request = root / "update-request.json"
            self.assertEqual(update.read_request(str(request), state), {"action": "install"})
            cases = [
                ({"action": "install", "version": "v2.6.0"}, {"action": "install", "version": "v2.6.0"}),
                ({"action": "rollback", "backup": "20261010T000000-v2.4.0"}, {"action": "rollback", "backup": "20261010T000000-v2.4.0"}),
            ]
            for payload, expected in cases:
                request.write_text(json.dumps(payload))
                self.assertEqual(update.read_request(str(request), state), expected)
                self.assertFalse(request.exists(), "задание должно удаляться после чтения")
            for payload in [{"action": "install", "version": "main; rm -rf /"}, {"action": "rollback", "backup": "../../etc"},
                            {"action": "rollback", "backup": "missing"}, {"action": "shell"}, {"action": "install", "extra": 1}]:
                request.write_text(json.dumps(payload))
                with self.subTest(payload=payload), self.assertRaises(RuntimeError):
                    update.read_request(str(request), state)
            target = root / "target.json"
            target.write_text(json.dumps({"action": "install"}))
            request.unlink(missing_ok=True)
            request.symlink_to(target)
            with self.assertRaises(RuntimeError):
                update.read_request(str(request), state)

    # Откат: текущая сборка сохраняется, резервная ставится и проверяется по коммиту.
    def test_rollback_installs_backup_and_keeps_current(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "bin").mkdir()
            executable = root / "bin/hkc-web"
            executable.write_bytes(b"\x7fELFcurrent")
            state = root / "state"
            backups = update.prepare_state(state)
            target = backups / "20261010T000000-v2.4.0"
            target.mkdir()
            (target / "hkc-web").write_bytes(b"\x7fELFold")
            old, new = "a" * 40, "b" * 40

            def fake_run(args, **kwargs):
                if args[-1] == "--version-json":
                    binary = Path(args[0]).read_bytes()
                    return json.dumps({"version": "v2.4.0", "commit": old} if binary.endswith(b"old") else {"version": "v2.5.0", "commit": new})
                return ""

            with patch.dict(os.environ, {"HKC_SOURCE_DIR": str(root), "HKC_UPDATE_DIR": str(state)}), \
                 patch.object(update, "run", side_effect=fake_run), patch.object(update, "git", return_value=new), \
                 patch.object(update, "await_health") as health:
                update.rollback(target.name)
            self.assertEqual(executable.read_bytes(), b"\x7fELFold")
            health.assert_called_with("http://127.0.0.1:8080", old)
            status = json.loads((state / "status.json").read_text())
            self.assertEqual(status["phase"], "complete")
            self.assertEqual(len(status["warnings"]), 1)
            saved = [item["version"] for item in json.loads((state / "backups.json").read_text())["backups"]]
            self.assertIn("v2.5.0", saved)

    # Дополнительная копия исходников не блокирует установку, а попадает в предупреждения.
    def test_optional_copy_is_warning(self):
        def fake_clean(path):
            if str(path).endswith("local"):
                raise RuntimeError("есть изменения")

        with patch.object(update, "require_clean", side_effect=fake_clean), patch.object(update, "git", return_value=""):
            warnings = update.sync_sources(Path("/tmp/state"), Path("/srv/source"), Path("/srv/local"), "c" * 40, "v2.6.0")
        self.assertEqual(len(warnings), 1)
        self.assertIn("Дополнительная копия", warnings[0])


    # Отмена до замены сборки: бинарник не тронут, итог «cancelled», флаг убран.
    def test_cancel_before_switch_changes_nothing(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "bin").mkdir()
            executable = root / "bin/hkc-web"
            executable.write_bytes(b"previous binary")
            data = root / "data"
            data.mkdir()
            old, new = "a" * 40, "b" * 40
            metadata = [{"version": "v2.1.0", "commit": old}, {"version": "v2.2.0", "commit": new}]

            def fake_run(args, **kwargs):
                if args[-1] == "--version-json":
                    return json.dumps(metadata.pop(0))
                return ""

            def fake_download(url, limit, progress=None):
                if progress:
                    (data / "update-cancel.json").write_text("{}")
                    progress(10, 100)
                return b"archive"

            env = {"HKC_SOURCE_DIR": str(root), "HKC_LOCAL_SOURCE_DIR": "", "HKC_UPDATE_DIR": str(root / "state"),
                   "HKC_AUTH_FILE": str(data / "web-auth.json")}
            with patch.dict(os.environ, env), patch.object(update, "require_clean"), \
                 patch.object(update, "git", return_value=new), patch.object(update, "run", side_effect=fake_run), \
                 patch.object(update, "download", side_effect=fake_download), patch.object(update, "await_health"):
                update.install("v2.2.0")
            self.assertEqual(executable.read_bytes(), b"previous binary")
            self.assertEqual(json.loads((root / "state/status.json").read_text())["phase"], "cancelled")
            self.assertFalse((data / "update-cancel.json").exists())

    # Параллельная загрузка частями собирает файл без потерь и сообщает общий прогресс.
    def test_parallel_download_reassembles_ranges(self):
        payload = bytes(range(256)) * 20000  # ~5 МБ
        calls = []

        class Response(io.BytesIO):
            def __init__(self, data, status=200, headers=None):
                super().__init__(data)
                self.status = status
                self.url = "https://cdn.example/asset"
                self.headers = headers or {}

            def __enter__(self):
                return self

            def __exit__(self, *args):
                return False

        def fake_urlopen(request, timeout=0):
            if request.get_method() == "HEAD":
                return Response(b"", headers={"Content-Length": str(len(payload)), "Accept-Ranges": "bytes"})
            first, last = map(int, request.headers["Range"].split("=")[1].split("-"))
            calls.append((first, last))
            return Response(payload[first:last + 1], status=206)

        seen = []
        with patch.object(update.urllib.request, "urlopen", side_effect=fake_urlopen):
            data = update.download("https://github.com/x", len(payload) + 1, lambda got, total: seen.append((got, total)))
        self.assertEqual(data, payload)
        self.assertEqual(len(calls), update.PARALLEL_STREAMS)
        self.assertEqual(seen[-1][1], len(payload))
        self.assertEqual(max(got for got, _ in seen), len(payload))

class KeepBotTests(unittest.TestCase):
    # Дополнение пишется один раз и перечитывается systemd только при изменении.
    def test_keep_bot_dropin_written_once(self):
        with tempfile.TemporaryDirectory() as temporary:
            dropin = Path(temporary) / "hkc-web.service.d" / "keep-bot.conf"
            calls = []
            with patch.object(update, "KEEP_BOT_DROPIN", dropin),                     patch.object(update, "run", lambda args, timeout=90: calls.append(args)),                     patch.object(update, "console_line", lambda state, text: None):
                update.keep_bot_on_restart(Path(temporary))
                update.keep_bot_on_restart(Path(temporary))
            self.assertIn("KillMode=process", dropin.read_text(encoding="utf-8"))
            self.assertEqual(calls, [["systemctl", "daemon-reload"]])

    # Ошибка записи не прерывает обновление.
    def test_keep_bot_dropin_failure_is_not_fatal(self):
        lines = []
        def fail(*args, **kwargs):
            raise RuntimeError("нет прав")
        with patch.object(update, "run", fail), patch.object(update, "console_line", lambda state, text: lines.append(text)):
            update.keep_bot_on_restart(Path("."))
        self.assertTrue(lines)


if __name__ == "__main__":
    unittest.main()
