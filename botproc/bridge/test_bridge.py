"""Проверки жизненного цикла на имитации загрузчика, без запуска Telegram."""
import asyncio
import inspect
import json
import os
import pathlib
import shutil
import subprocess
import tempfile
import types
import unittest
import venv
from unittest.mock import patch
from hkc_modules_bridge import Monitor


class SelfSuspend(Exception):
    pass


class SelfUnload(Exception):
    pass


class BridgeTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        class Modules:
            def __init__(self):
                self.client = types.SimpleNamespace(tg_id=123)
                self.modules = []

            async def register_module(self, spec, module_name, origin="<core>", **kwargs):
                if spec == "import-error":
                    raise ImportError("missing dependency")
                async def client_ready():
                    if spec == "suspend": raise SelfSuspend("disabled")
                    if spec == "unload": raise SelfUnload("unloaded")
                    if spec == "error": raise ValueError("bad configuration")
                instance = types.SimpleNamespace(name="Example", client_ready=client_ready, __version__=(1,2,3))
                self.modules.append(instance)
                return instance

            async def send_ready_one(self, instance):
                try: await instance.client_ready()
                except SelfSuspend: return
                except SelfUnload: self.modules.remove(instance)

            async def unload_module(self, name):
                self.modules.clear()
                return [name]

            async def _register_modules(self, files, origin="<core>"):
                return []

        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.monitor = Monitor(self.temp.name)
        self.monitor.patch(types.SimpleNamespace(Modules=Modules, SelfSuspend=SelfSuspend, SelfUnload=SelfUnload))
        self.manager = Modules()

    async def test_ready_signature_and_unload(self):
        instance = await self.manager.register_module("ok", "example")
        self.assertEqual(len(inspect.signature(instance.client_ready).parameters), 0)
        await self.manager.send_ready_one(instance)
        self.assertEqual(self.monitor.rows["123:example"]["state"], "ready")
        await self.manager.unload_module("Example")
        self.assertEqual(self.monitor.rows["123:example"]["state"], "unloaded")
        self.monitor.write()

    async def test_failed_import(self):
        with self.assertRaises(ImportError): await self.manager.register_module("import-error", "broken")
        self.assertEqual(self.monitor.rows["123:broken"]["state"], "error")

    async def test_initialization_states(self):
        for spec, expected in [("suspend", "suspended"), ("unload", "unloaded"), ("error", "error")]:
            instance = await self.manager.register_module(spec, spec)
            try: await self.manager.send_ready_one(instance)
            except ValueError: pass
            self.assertEqual(self.monitor.rows["123:" + spec]["state"], expected)

    async def test_writer_does_not_fake_main_loop_heartbeat(self):
        self.monitor.observe_loop()
        heartbeat = self.monitor.loop_at
        self.assertGreater(heartbeat, 0)
        with patch("hkc_modules_bridge.time.time", return_value=heartbeat + 181):
            self.monitor.write()
        data = json.loads(pathlib.Path(self.monitor.path).read_text(encoding="utf-8"))
        self.assertEqual(data["loopAt"], heartbeat)
        self.assertGreater(data["sampledAt"] - data["loopAt"], 180)

    async def test_expected_external_download_failure_and_success(self):
        manager = self.manager
        class LoaderMod:
            allmodules = manager
            async def _get_modules_to_load(self):
                return {"Weather": "https://example.org/weather.py", "Broken": "https://example.org/broken.py"}
            async def download_and_install(self, module_name, *args, **kwargs):
                if "broken" in module_name: return 0
                return await self.load_module("source", None, "Weather", module_name)
            async def load_module(self, doc, message, name=None, origin="<string>", *args, **kwargs):
                instance = await manager.register_module("ok", "dynamic", origin)
                await manager.send_ready_one(instance)
                return True
        self.monitor.patch_external(types.SimpleNamespace(LoaderMod=LoaderMod))
        loader = LoaderMod()
        todo = await loader._get_modules_to_load()
        self.assertEqual(len(self.monitor.rows), 2)
        for origin in todo.values(): await loader.download_and_install(origin)
        self.assertEqual(len(self.monitor.rows), 2)
        self.assertEqual(sorted(row["state"] for row in self.monitor.rows.values()), ["error", "ready"])
        self.assertIsNone(self.monitor.installing.get())


class StartupTest(unittest.TestCase):
    def test_pth_hooks_real_module_startup_but_not_other_python_commands(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = pathlib.Path(temporary)
            environment = root / ".venv"
            venv.EnvBuilder(with_pip=False).create(environment)
            python = environment / ("Scripts/python.exe" if os.name == "nt" else "bin/python3")
            site = pathlib.Path(subprocess.check_output([str(python), "-c", "import site; print(site.getsitepackages()[0])"], text=True).strip())
            shutil.copyfile(pathlib.Path(__file__).with_name("hkc_modules_bridge.py"), site / "hkc_modules_bridge.py")
            (site / "hkc_modules_bridge.pth").write_text("import hkc_modules_bridge; hkc_modules_bridge.install(" + repr(str(root)) + ")\n", encoding="utf-8")
            check = subprocess.check_output([str(python), "-c", "import sys; print(hasattr(sys, '_hkc_modules_monitor'))"], text=True)
            self.assertEqual(check.strip(), "False")
            package = root / "heroku"
            package.mkdir()
            (package / "__init__.py").write_text("", encoding="utf-8")
            (package / "loader.py").write_text('''
class SelfSuspend(Exception): pass
class SelfUnload(Exception): pass
class Instance:
    name = "Integration"
    async def client_ready(self): pass
class Modules:
    def __init__(self): self.modules = []
    async def register_module(self, spec, module_name, origin="<core>"):
        result = Instance(); self.modules.append(result); return result
    async def send_ready_one(self, instance): await instance.client_ready()
    async def unload_module(self, name): self.modules.clear(); return [name]
    async def _register_modules(self, files, origin="<core>"): return []
''', encoding="utf-8")
            (package / "__main__.py").write_text('''
import asyncio, sys
from .loader import Modules
async def main():
    manager = Modules()
    instance = await manager.register_module(None, "integration")
    await manager.send_ready_one(instance)
    sys._hkc_modules_monitor.write()
asyncio.run(main())
''', encoding="utf-8")
            subprocess.run([str(python), "-m", "heroku"], cwd=root, check=True)
            data = json.loads((root / ".hkc-modules.json").read_text(encoding="utf-8"))
            self.assertEqual(data["modules"][0]["state"], "ready")
            self.assertEqual(data["modules"][0]["name"], "Integration")


if __name__ == "__main__":
    unittest.main()
