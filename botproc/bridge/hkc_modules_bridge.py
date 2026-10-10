"""Наблюдение за Heroku без изменения исходников и поведения загрузчика."""
import contextvars
import asyncio
import functools
import hashlib
import importlib.abc
import importlib.machinery
import json
import os
import sys
import threading
import time
import uuid


class Monitor:
    def __init__(self, root):
        self.path = os.path.join(root, ".hkc-modules.json")
        self.session = uuid.uuid4().hex
        self.rows = {}
        self.instances = {}
        self.lock = threading.RLock()
        self.installing = contextvars.ContextVar("hkc_installing", default=None)
        self.loop_at = 0
        self.loops = set()

    def observe_loop(self):
        loop = asyncio.get_running_loop()
        if loop in self.loops:
            return
        self.loops.add(loop)

        def pulse():
            with self.lock:
                self.loop_at = time.time()
            loop.call_later(1, pulse)
        pulse()

    def update(self, key, **values):
        with self.lock:
            row = self.rows.setdefault(key, dict(id=key, name=key.split(":", 1)[-1],
                                                kind="external", state="loading", version="", error=""))
            row.update(values)

    def key(self, manager, name):
        return str(getattr(getattr(manager, "client", None), "tg_id", "default")) + ":" + name

    def snapshot(self):
        with self.lock:
            return dict(schema=1, pid=os.getpid(), session=self.session,
                        sampledAt=time.time(), loopAt=self.loop_at,
                        modules=[dict(row) for row in self.rows.values()])

    def write(self):
        # Замена атомарна: HTTP-сервер никогда не читает половину JSON.
        temporary = self.path + "." + str(os.getpid()) + ".tmp"
        descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            json.dump(self.snapshot(), handle, ensure_ascii=False)
        os.replace(temporary, self.path)

    def heartbeat(self):
        while True:
            try:
                self.write()
            except OSError:
                pass  # Мониторинг не должен мешать боту при недоступном диске.
            time.sleep(1)

    def patch(self, module):
        cls = module.Modules
        original_register = cls.register_module
        original_ready = cls.send_ready_one
        original_unload = cls.unload_module
        monitor = self

        @functools.wraps(original_register)
        async def register(manager, spec, module_name, origin="<core>", *args, **kwargs):
            monitor.observe_loop()
            key = monitor.installing.get() or monitor.key(manager, module_name)
            label = monitor.rows[key]["name"] if monitor.installing.get() and key in monitor.rows else module_name.rsplit(".", 1)[-1]
            before = list(manager.modules)
            monitor.update(key, name=label,
                           kind="core" if origin.startswith("<core") else "external",
                           state="loading", error="")
            try:
                instance = await original_register(manager, spec, module_name, origin, *args, **kwargs)
            except Exception as error:
                monitor.update(key, state="error", error=type(error).__name__ + ": " + str(error)[:2000])
                raise
            monitor.instances[id(instance)] = key
            for previous in before:
                previous_key = monitor.instances.get(id(previous))
                if previous not in manager.modules:
                    if previous_key and previous_key != key:
                        monitor.update(previous_key, state="unloaded")
                    monitor.instances.pop(id(previous), None)
            name = getattr(instance, "name", None) or instance.__class__.__name__
            try:
                name = instance.strings["name"]
            except (KeyError, TypeError, AttributeError):
                pass
            version = getattr(instance, "__version__", "")
            if isinstance(version, (tuple, list)):
                version = ".".join(map(str, version))
            monitor.update(key, name=str(name), version=str(version))
            # Встроенные модули исполняются через StringLoader, минуя meta_path.
            if instance.__class__.__name__ == "LoaderMod":
                monitor.patch_external(sys.modules[instance.__class__.__module__])
            # client_ready может выбросить SelfSuspend/SelfUnload, которые загрузчик
            # обрабатывает сам. Обёртка наблюдает исключение до его обработки.
            ready_hook = instance.client_ready

            @functools.wraps(ready_hook)
            async def observed_ready(*hook_args, **hook_kwargs):
                try:
                    return await ready_hook(*hook_args, **hook_kwargs)
                except Exception as error:
                    state = "suspended" if isinstance(error, module.SelfSuspend) else (
                        "unloaded" if isinstance(error, module.SelfUnload) else "error")
                    monitor.update(key, state=state, error=type(error).__name__ + ": " + str(error)[:2000])
                    raise
            instance.client_ready = observed_ready
            return instance

        @functools.wraps(original_ready)
        async def ready(manager, instance, *args, **kwargs):
            key = monitor.instances.get(id(instance))
            try:
                result = await original_ready(manager, instance, *args, **kwargs)
            except Exception as error:
                if key and monitor.rows[key]["state"] == "loading":
                    monitor.update(key, state="error", error=type(error).__name__ + ": " + str(error)[:2000])
                raise
            if key and monitor.rows[key]["state"] == "loading":
                monitor.update(key, state="ready" if instance in manager.modules else "unloaded")
            return result

        @functools.wraps(original_unload)
        async def unload(manager, *args, **kwargs):
            before = list(manager.modules)
            result = await original_unload(manager, *args, **kwargs)
            for instance in before:
                key = monitor.instances.get(id(instance))
                if key and instance not in manager.modules:
                    monitor.update(key, state="unloaded")
                    monitor.instances.pop(id(instance), None)
            return result

        cls.register_module = register
        cls.send_ready_one = ready
        cls.unload_module = unload

        # Показываем весь ожидаемый набор файлов ещё до последовательного импорта.
        original_files = cls._register_modules

        @functools.wraps(original_files)
        async def files(manager, modules, origin="<core>"):
            for filename in modules:
                name = os.path.basename(filename).rsplit(".py", 1)[0]
                monitor.update(monitor.key(manager, "heroku.modules." + name), name=name,
                               kind="core" if origin.startswith("<core") else "external")
            return await original_files(manager, modules, origin)
        cls._register_modules = files

    def external_key(self, manager, origin):
        return self.key(manager, "external-" + hashlib.sha256(str(origin).encode()).hexdigest()[:24])

    def patch_external(self, module):
        cls = module.LoaderMod
        if getattr(cls, "_hkc_observed", False):
            return
        cls._hkc_observed = True
        original_todo = cls._get_modules_to_load
        original_download = cls.download_and_install
        original_load = cls.load_module
        monitor = self

        @functools.wraps(original_todo)
        async def todo(loader, *args, **kwargs):
            result = await original_todo(loader, *args, **kwargs)
            for name, origin in result.items():
                monitor.update(monitor.external_key(loader.allmodules, origin), name=str(name))
            return result

        @functools.wraps(original_download)
        async def download(loader, module_name, *args, **kwargs):
            key = monitor.external_key(loader.allmodules, module_name.strip())
            name = kwargs.get("name") or module_name.split("?")[0].rstrip("/").rsplit("/", 1)[-1].removesuffix(".py")
            monitor.update(key, name=str(name), state="loading", error="")
            token = monitor.installing.set(key)
            try:
                result = await original_download(loader, module_name, *args, **kwargs)
                if not result and monitor.rows[key]["state"] not in ("suspended", "unloaded"):
                    # Загрузчик может поглотить исключение; подробность тогда остаётся в журнале.
                    monitor.update(key, state="error", error=monitor.rows[key]["error"] or "Загрузчик не смог установить модуль. Подробности — в журнале Heroku.")
                return result
            except Exception as error:
                monitor.update(key, state="error", error=type(error).__name__ + ": " + str(error)[:2000])
                raise
            finally:
                monitor.installing.reset(token)
        cls._get_modules_to_load = todo
        cls.download_and_install = download

        @functools.wraps(original_load)
        async def load(loader, doc, message, name=None, origin="<string>", *args, **kwargs):
            inherited = monitor.installing.get()
            key = inherited or monitor.external_key(loader.allmodules, origin if origin != "<string>" else hashlib.sha256(doc.encode()).hexdigest())
            if not inherited:
                monitor.update(key, name=name or "Модуль из файла", state="loading", error="")
            token = monitor.installing.set(key)
            try:
                result = await original_load(loader, doc, message, name, origin, *args, **kwargs)
                if not result and monitor.rows[key]["state"] not in ("suspended", "unloaded"):
                    monitor.update(key, state="error", error=monitor.rows[key]["error"] or "Загрузчик отклонил модуль. Подробности — в журнале Heroku.")
                return result
            except Exception as error:
                monitor.update(key, state="error", error=type(error).__name__ + ": " + str(error)[:2000])
                raise
            finally:
                monitor.installing.reset(token)
        cls.load_module = load


class BridgeFinder(importlib.abc.MetaPathFinder):
    def __init__(self, monitor):
        self.monitor = monitor

    def find_spec(self, fullname, path=None, target=None):
        if fullname not in ("heroku.loader", "heroku.modules.loader"):
            return None
        spec = importlib.machinery.PathFinder.find_spec(fullname, path)
        if spec is None or spec.loader is None:
            return None
        original = spec.loader
        monitor = self.monitor

        class ObservedLoader(importlib.abc.Loader):
            def create_module(self, spec):
                return original.create_module(spec)

            def exec_module(self, module):
                original.exec_module(module)
                if fullname == "heroku.loader":
                    monitor.patch(module)
                else:
                    monitor.patch_external(module)

        spec.loader = ObservedLoader()
        return spec


def install(root):
    # .pth выполняется и для pip/диагностики; наблюдаем только python -m heroku.
    arguments = getattr(sys, "orig_argv", [])
    if not any(arguments[i:i + 2] == ["-m", "heroku"] for i in range(len(arguments))):
        return
    if getattr(sys, "_hkc_modules_monitor", None):
        return
    monitor = Monitor(root)
    sys._hkc_modules_monitor = monitor
    sys.meta_path.insert(0, BridgeFinder(monitor))
    threading.Thread(target=monitor.heartbeat, name="hkc-modules", daemon=True).start()
