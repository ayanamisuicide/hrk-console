// Отдельная страница установки наблюдает за службой даже при перезапуске самой панели.
const $ = (selector) => document.querySelector(selector);
document.documentElement.dataset.theme =
  localStorage.getItem("hkc-theme") || "dark";
let token = sessionStorage.getItem("hkc-admin-token") || "";
let overview;
// Пока отдельная служба запускается, старый сохранённый результат не считаем новым заданием.
let waitingForJob = 0;
// Сигнатура сохраняет DOM истории, если секундный опрос не принёс новых событий.
let timelineSignature = "";
// Перезапуск панели временно рвёт HTTP; повторный опрос продолжает наблюдение после её запуска.
let progressBusy = false;
let lastTerminalStamp = "";
let lastRenderedPhase = null;

// Повторно запускает визуальный переход этапа, если движение разрешено.
function replayPhaseMotion(element) {
  if (!element || window.prefersReducedMotion?.()) return;
  element.classList.remove("phase-change");
  void element.offsetWidth;
  element.classList.add("phase-change");
  element.addEventListener(
    "animationend",
    () => element.classList.remove("phase-change"),
    { once: true },
  );
}

// Форматирует время этапа установки на русском.
function dateTime(value) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? "—"
    : new Intl.DateTimeFormat("ru-RU", {
        dateStyle: "short",
        timeStyle: "medium",
      }).format(date);
}
// Сокращает идентификатор коммита для отображения.
function short(value) {
  return value ? value.slice(0, 12) : "—";
}
// Запрашивает административный токен и показывает сообщение о доступе.
function openAuth(message = "") {
  $("#update-auth-error").textContent = message;
  if (!$("#update-auth").open) $("#update-auth").showModal();
}
// Выполняет административный запрос страницы обновления и возвращает диалог входа при HTTP 401.
async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${token}`);
  const response = await fetch(path, {
    ...options,
    headers,
    cache: "no-store",
  });
  const data = await response.json();
  if (response.status === 401) {
    token = "";
    sessionStorage.removeItem("hkc-admin-token");
    openAuth("Токен недействителен. Введите его снова.");
  }
  if (!response.ok) throw new Error(data.message || `HTTP ${response.status}`);
  return data;
}

// Рисует историю этапов установки только при изменении набора событий.
function renderTimeline(events) {
  const signature = JSON.stringify(events);
  if (signature === timelineSignature) return;
  const hasRenderedTimeline = timelineSignature !== "";
  timelineSignature = signature;
  const list = $("#timeline-list");
  list.replaceChildren();
  $("#timeline-count").textContent = `${events.length} событий`;
  if (!events.length) {
    const empty = document.createElement("li");
    empty.className = "timeline-empty";
    empty.textContent = "После запуска здесь появится подробный ход установки.";
    list.append(empty);
    return;
  }
  for (const [index, event] of events.entries()) {
    const item = document.createElement("li");
    item.dataset.phase = event.phase || "checking";
    if (
      hasRenderedTimeline &&
      index === events.length - 1 &&
      !window.prefersReducedMotion?.()
    )
      item.classList.add("timeline-enter");
    const message = document.createElement("strong");
    message.textContent = event.message || "Этап выполнен";
    const stamp = document.createElement("time");
    stamp.textContent = `${dateTime(event.at)} · ${event.progress ?? 0}%`;
    item.append(message, stamp);
    list.append(item);
  }
  list.scrollTop = list.scrollHeight;
}

// Показывает фазу, прогресс, результат и резервную копию установки.
function renderJob(job) {
  if (
    waitingForJob &&
    (!job?.updatedAt || Date.parse(job.updatedAt) < waitingForJob)
  )
    return;
  if (waitingForJob) waitingForJob = 0;
  const phase = job?.phase || "idle";
  const labels = {
    idle: "ОЖИДАНИЕ",
    checking: "ПРОВЕРКА",
    downloading: "ЗАГРУЗКА",
    restarting: "ПЕРЕЗАПУСК",
    complete: "ГОТОВО",
    rolled_back: "ОТКАТ",
    failed: "ОШИБКА",
  };
  const fallback = {
    checking: 12,
    downloading: 44,
    restarting: 78,
    complete: 100,
    rolled_back: 100,
    failed: 100,
  };
  const percent = Math.max(
    0,
    Math.min(100, Number(job?.progress ?? fallback[phase] ?? 0)),
  );
  const phaseChanged =
    lastRenderedPhase !== null && phase !== lastRenderedPhase;
  lastRenderedPhase = phase;
  $("#update-state").dataset.phase = phase;
  $("#update-state").textContent = labels[phase] || phase.toUpperCase();
  $("#update-title").textContent =
    phase === "complete"
      ? "Обновление завершено"
      : phase === "rolled_back"
        ? "Предыдущая версия восстановлена"
        : phase === "failed"
          ? "Установка остановлена"
          : phase === "idle"
            ? "Готово к проверке"
            : "Установка выполняется";
  if (phaseChanged) {
    replayPhaseMotion($("#update-state"));
    replayPhaseMotion($("#update-title"));
  }
  $("#update-message").textContent =
    job?.message || "Выберите действие, чтобы проверить или установить релиз.";
  $("#update-clock").textContent = job?.updatedAt
    ? `Обновлено ${dateTime(job.updatedAt)}`
    : "—";
  $("#update-progress-bar").style.width = `${percent}%`;
  $(".update-progress-track").setAttribute("aria-valuenow", String(percent));
  $("#update-progress-label").textContent = `${percent}%`;
  $("#update-target").textContent =
    `Релиз ${job?.version || overview?.github?.version || "—"}`;
  $("#update-backup").hidden = !job?.backup;
  $("#update-backup").textContent = job?.backup
    ? `Предыдущая сборка: ${job.backup}`
    : "";
  const events = Array.isArray(job?.events)
    ? job.events
    : job
      ? [{ at: job.updatedAt, phase, message: job.message, progress: percent }]
      : [];
  renderTimeline(events);
  if (
    ["complete", "rolled_back", "failed"].includes(phase) &&
    job.updatedAt !== lastTerminalStamp
  ) {
    lastTerminalStamp = job.updatedAt;
    refreshOverview().catch(() => {});
  }
}

// Сравнивает сборку, релиз и исходники, вычисляя доступность запуска установки.
function renderOverview(data) {
  overview = data;
  const remote = data.github || {};
  $("#version-running").textContent = data.installed?.version || "—";
  $("#commit-running").textContent = short(data.installed?.commit);
  $("#version-release").textContent =
    remote.version || (remote.checking ? "Проверяем…" : "—");
  $("#commit-release").textContent = remote.error || short(remote.commit);
  const copies = [data.source, data.local].filter((copy) => copy?.configured);
  $("#version-copies").textContent = copies.length
    ? copies.every(
        (copy) =>
          copy.commit === data.installed?.commit && !copy.dirty && !copy.error,
      )
      ? "Совпадают"
      : "Требуют внимания"
    : "Не подключены";
  $("#commit-copies").textContent =
    copies
      .map((copy) => `${copy.branch || "—"}: ${short(copy.commit)}`)
      .join(" · ") || "—";
  const blocked = copies.some(
    (copy) => copy.dirty || copy.error || copy.branch !== "main",
  );
  const busy = ["checking", "downloading", "restarting"].includes(
    data.job?.phase,
  );
  const syncNeeded = copies.some((copy) => copy.commit !== remote.commit);
  const ready =
    data.enabled &&
    !blocked &&
    !busy &&
    !remote.error &&
    !remote.checking &&
    !!remote.commit &&
    (remote.commit !== data.installed?.commit || syncNeeded) &&
    !data.installed?.modified;
  $("#update-install").disabled = !ready;
  $("#update-install").textContent =
    remote.commit === data.installed?.commit && syncNeeded
      ? "Синхронизировать копии"
      : "Установить обновление";
  $("#update-readiness").textContent = !data.enabled
    ? "Установщик на этом сервере не настроен."
    : blocked
      ? "Установка заблокирована: одна из копий изменена, недоступна или не на ветке main."
      : busy
        ? "Установка уже выполняется. Следите за этапами ниже."
        : remote.error
          ? `Сверка GitHub не удалась: ${remote.error}`
          : remote.checking
            ? "Проверяем GitHub…"
            : !remote.commit
              ? "Ожидаем сведения о релизе."
              : data.installed?.modified
                ? "Запущенная сборка содержит локальные изменения."
                : !ready
                  ? "Версии совпадают. Устанавливать ничего не нужно."
                  : "Всё готово. Перед установкой будет запрошено подтверждение.";
  if (!waitingForJob) renderJob(data.job);
}

// Обновляет сведения об установленной версии и доступном релизе.
async function refreshOverview() {
  if (!token) {
    openAuth();
    return;
  }
  const data = await api("/api/admin/updates");
  renderOverview(data);
}
// Обновляет состояние отдельной службы установки, включая период недоступности панели.
async function refreshProgress() {
  if (!token || progressBusy) return;
  progressBusy = true;
  try {
    const data = await api("/api/admin/updates/progress");
    $("#update-connection").textContent = "● Соединение активно";
    $("#version-running").textContent = data.installed?.version || "—";
    $("#commit-running").textContent = short(data.installed?.commit);
    renderJob(data.job);
  } catch (error) {
    $("#update-connection").textContent =
      "○ Переподключаемся: " + error.message;
  } finally {
    progressBusy = false;
  }
}

$("#update-auth-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  token = $("#update-token").value.trim();
  try {
    await refreshOverview();
    sessionStorage.setItem("hkc-admin-token", token);
    $("#update-auth").close();
  } catch (error) {
    $("#update-auth-error").textContent = error.message;
  }
});
$("#update-check").addEventListener("click", async () => {
  $("#update-check").disabled = true;
  try {
    await api("/api/admin/updates/check", { method: "POST" });
    await refreshOverview();
    setTimeout(refreshOverview, 1800);
  } catch (error) {
    $("#update-message").textContent = error.message;
  } finally {
    $("#update-check").disabled = false;
  }
});
$("#update-install").addEventListener("click", () =>
  $("#update-confirm").showModal(),
);
$("#update-confirm-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const confirmed = event.submitter?.value === "confirm";
  $("#update-confirm").close();
  if (!confirmed) return;
  $("#update-install").disabled = true;
  waitingForJob = Date.now() - 2000;
  $("#update-state").dataset.phase = "checking";
  $("#update-state").textContent = "ЗАПУСК";
  $("#update-title").textContent = "Запускаем установку";
  $("#update-message").textContent = "Отправляем команду службе обновления…";
  try {
    await api("/api/admin/updates/install", { method: "POST" });
    await refreshProgress();
  } catch (error) {
    waitingForJob = 0;
    $("#update-message").textContent = error.message;
    await refreshOverview().catch(() => {});
  }
});

refreshOverview().catch((error) => {
  $("#update-connection").textContent = error.message;
});
setInterval(refreshProgress, 1500);
setInterval(() => {
  if (token && !waitingForJob) refreshOverview().catch(() => {});
}, 15000);
