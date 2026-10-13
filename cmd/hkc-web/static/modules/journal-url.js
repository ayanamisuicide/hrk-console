// Журнал: запрос и открытая запись в адресе страницы.
import { entryKey } from "./journal-format.js";

export function createJournalUrl(J) {
  const { ctx, S } = J;
  // Части журнала вызывают друг друга через J: функции других частей появляются в нём
  // при сборке журнала, а вызываются уже после неё.

  /* ---------- Адрес страницы ---------- */

  // Запрос журнала и открытая запись живут в адресе (#logs?level=ERROR&module=…&entry=…): ссылкой
  // можно поделиться, а перезагрузка страницы не сбрасывает фильтры. Адрес заменяется, а не
  // добавляется в историю, чтобы «Назад» не листал каждую букву поиска.
  function urlParams() {
    const p = J.baseParams();
    if (S.range) {
      p.set("from", String(Math.floor(S.range.from)));
      if (S.range.to != null) p.set("to", String(Math.ceil(S.range.to)));
    }
    if (S.drawerMode === "entry" && S.selected) {
      const at = S.selected.indexOf("|");
      p.set("entry", `${S.selected.slice(0, at)}~${S.selected.slice(at + 1)}`);
    }
    return p;
  }

  function writeUrl() {
    if (ctx.currentView !== "logs" || !J.dom) return;
    const p = urlParams().toString();
    const hash = p ? `#logs?${p}` : "#logs";
    if (location.hash !== hash) history.replaceState(history.state, "", `${location.pathname}${location.search}${hash}`);
  }

  function entryLink(e) {
    return `${location.origin}${location.pathname}#logs?entry=${e.ts}~${encodeURIComponent(e.id)}`;
  }

  // Разбирает адрес; true, если он относится к журналу.
  function readUrl() {
    const m = location.hash.match(/^#logs(?:\?(.*))?$/);
    if (!m) return false;
    const p = new URLSearchParams(m[1] || "");
    const level = (p.get("level") || "").toUpperCase();
    S.level = ["ERROR", "WARNING", "INFO"].includes(level) ? level : "ALL";
    S.module = p.get("module") || "";
    S.text = p.get("q") || "";
    if (J.dom) J.dom.input.value = S.text;
    const from = Number(p.get("from")) || 0;
    const to = Number(p.get("to")) || 0;
    S.range = from ? { from, to: to > from ? to : null } : null;
    S.bookmarksMode = false;
    S.showHidden = p.get("hidden") === "1";
    S.live = !S.range || S.range.to == null;
    S.pendingEntry = null;
    const entry = (p.get("entry") || "").match(/^(\d+)~(.+)$/);
    if (entry) S.pendingEntry = { ts: Number(entry[1]), id: entry[2] };
    return true;
  }

  // Открывает запись из ссылки: показывает её место в ленте (диапазон ±15 минут, если её нет среди
  // загруженных) и панель деталей. Если запись не подходит под фильтры, панель всё равно открывается.
  async function openPendingEntry() {
    const target = S.pendingEntry;
    S.pendingEntry = null;
    if (!target) return;
    const key = `${target.ts}|${target.id}`;
    const loaded = () => S.entries.find((e) => entryKey(e) === key);
    if (!loaded() && !S.bookmarksMode) {
      const pad = 15 * 60000;
      S.range = { from: target.ts - pad, to: Math.min(target.ts + pad, Math.floor(J.clock())) };
      S.live = false;
      J.syncModeButtons();
      J.renderChips();
      S.timeline?.setSelection([S.range.from, S.range.to]);
      await J.reloadNow();
    }
    let entry = loaded();
    if (!entry) {
      try {
        const data = await ctx.request(`/api/logs/context?ts=${target.ts}&id=${encodeURIComponent(target.id)}&before=0&after=0`);
        entry = data.entries?.[data.index];
      } catch (_) {
        /* Ниже — сообщение о ротации. */
      }
    }
    if (!entry) {
      ctx.showNotice("Запись из ссылки не найдена: журнал мог ротироваться", "error");
      return;
    }
    const group = S.groups.find((g) => g.entries.some((item) => entryKey(item) === key));
    if (group?.node) {
      S.stick = false;
      group.node.scrollIntoView({ block: "center" });
      group.node.classList.add("focused");
      setTimeout(() => group.node.classList.remove("focused"), 2400);
    }
    J.openDrawer(entry);
  }

  // Вызывается точкой входа: раздел из адреса важнее запомненного в браузере.
  function initialView() {
    return /^#logs(\?|$)/.test(location.hash) ? "logs" : "";
  }

  return { writeUrl, entryLink, readUrl, openPendingEntry, initialView };
}
