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


def archive(name="hkc-web", kind=tarfile.REGTYPE, payload=b"\x7fELFtest"):
    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode="w:gz") as tar:
        item = tarfile.TarInfo(name)
        item.type = kind
        item.size = len(payload) if kind == tarfile.REGTYPE else 0
        tar.addfile(item, io.BytesIO(payload) if item.size else None)
    return data.getvalue()


class ReleaseTests(unittest.TestCase):
    def verify(self, data):
        checksum = (hashlib.sha256(data).hexdigest() + "  release.tar.gz\n").encode()
        return update.verify_archive(data, checksum, "release.tar.gz")

    def test_valid_binary(self):
        self.assertEqual(self.verify(archive()), b"\x7fELFtest")

    def test_reject_paths_links_and_non_elf(self):
        for data in [archive("../hkc-web"), archive(kind=tarfile.SYMTYPE), archive(payload=b"not ELF")]:
            with self.subTest(data=data[:8]), self.assertRaises(RuntimeError):
                self.verify(data)

    def test_reject_tampered_archive(self):
        with self.assertRaises(RuntimeError):
            update.verify_archive(archive(), b"0" * 64 + b"  release.tar.gz", "release.tar.gz")

    def test_reject_wrong_checksum_filename(self):
        data = archive()
        with self.assertRaises(RuntimeError):
            update.verify_archive(data, (hashlib.sha256(data).hexdigest() + "  wrong.tar.gz").encode(), "release.tar.gz")

    def test_health_failure_restores_previous_binary_before_source_merge(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "bin").mkdir()
            executable = root / "bin/hkc-web"
            executable.write_bytes(b"previous binary")
            old, new = "a" * 40, "b" * 40
            metadata = [{"version":"v2.1.0","commit":old}, {"version":"v2.2.0","commit":new}]
            calls = []

            def fake_run(args, **kwargs):
                calls.append(args)
                if args[-1] == "--version-json":
                    return json.dumps(metadata.pop(0))
                return ""

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


if __name__ == "__main__":
    unittest.main()
