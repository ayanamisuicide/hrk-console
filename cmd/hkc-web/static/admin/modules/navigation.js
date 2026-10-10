// Тема, подтверждения и анимированное дерево разделов администрирования.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createNavigation(ctx) {
  // Сохраняет имена раскрытых ветвей админки в localStorage.
  function saveTreeState() {
    const open = [...document.querySelectorAll(".admin-tree-group[open]")].map(
      (item) => item.dataset.tree,
    );
    localStorage.setItem("hkc-admin-tree", JSON.stringify(open));
  }

  // Возвращает Promise результата модального подтверждения, не блокируя поток браузера.
  function confirmAction(title, message) {
    const dialog = ctx.$("#confirm-dialog");
    ctx.$("#confirm-title").textContent = title;
    ctx.$("#confirm-message").textContent = message;
    dialog.showModal();
    return new Promise((resolve) => {
      dialog.addEventListener(
        "close",
        () => resolve(dialog.returnValue === "confirm"),
        { once: true },
      );
    });
  }

  // Форматирует время на русском с учётом отсутствующего значения.
  function formatDate(value) {
    if (!value) return "никогда";
    return new Intl.DateTimeFormat("ru-RU", {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(new Date(value));
  }

  // Показывает сообщение через общий помощник анимации и автоматического скрытия.
  function showNotice(message, kind = "ok") {
    window.motionNotice(ctx.$("#admin-notice"), message, kind);
  }
  // Переключает тему админки и общую сохранённую настройку.
  function bindAdminTheme() {
    ctx.$("#admin-theme").addEventListener("click", () => {
      const theme =
        document.documentElement.dataset.theme === "dark" ? "light" : "dark";
      window.motionTheme?.();
      document.documentElement.dataset.theme = theme;
      localStorage.setItem("hkc-theme", theme);
    });
  }
  // Анимирует высоту ветви, отменяет незавершённый переход и сохраняет раскрытие; уменьшенное движение
  // переключает сразу.
  function bindAdminTreeGroup() {
    document.querySelectorAll(".admin-tree-group").forEach((branch) => {
      const summary = branch.querySelector(":scope > summary");
      const clip = branch.querySelector(":scope > .admin-tree-clip");
      let frame = 0;
      let motionTimer;
      let motionEnd;
      const cancelMotion = () => {
        cancelAnimationFrame(frame);
        clearTimeout(motionTimer);
        if (motionEnd && clip)
          clip.removeEventListener("transitionend", motionEnd);
        motionEnd = null;
      };
      summary.setAttribute("aria-expanded", String(branch.open));
      summary.addEventListener("click", (event) => {
        event.preventDefault();
        const visuallyOpen =
          branch.open && !branch.classList.contains("is-closing");
        const open = !visuallyOpen;
        cancelMotion();
        if (open) {
          const startHeight =
            branch.open && clip ? clip.getBoundingClientRect().height : 0;
          if (clip) clip.style.height = `${startHeight}px`;
          branch.classList.remove("is-closing");
          if (clip) clip.inert = false;
          branch.open = true;
          summary.setAttribute("aria-expanded", "true");
          if (clip && !window.prefersReducedMotion?.()) {
            void clip.offsetHeight;
            frame = requestAnimationFrame(() => {
              clip.style.height = `${clip.scrollHeight}px`;
            });
            const finish = (transitionEvent) => {
              if (
                transitionEvent &&
                (transitionEvent.target !== clip ||
                  transitionEvent.propertyName !== "height")
              )
                return;
              cancelMotion();
              clip.style.height = "";
            };
            motionEnd = finish;
            clip.addEventListener("transitionend", finish);
            motionTimer = setTimeout(() => finish(), 700);
          } else if (clip) clip.style.height = "";
          ctx.saveTreeState();
          return;
        }
        summary.setAttribute("aria-expanded", "false");
        if (
          window.prefersReducedMotion?.() ||
          window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ||
          !branch.open ||
          !clip
        ) {
          branch.open = false;
          if (clip) {
            clip.inert = true;
            clip.style.height = "";
          }
          branch.classList.remove("is-closing");
          ctx.saveTreeState();
          return;
        }
        clip.inert = true;
        clip.style.height = `${clip.getBoundingClientRect().height}px`;
        void clip.offsetHeight;
        branch.classList.add("is-closing");
        const finish = (transitionEvent) => {
          if (
            transitionEvent &&
            (transitionEvent.target !== clip ||
              transitionEvent.propertyName !== "height")
          )
            return;
          if (!branch.classList.contains("is-closing")) return;
          cancelMotion();
          branch.open = false;
          clip.style.height = "";
          branch.classList.remove("is-closing");
          ctx.saveTreeState();
        };
        motionEnd = finish;
        clip.addEventListener("transitionend", finish);
        frame = requestAnimationFrame(() => {
          clip.style.height = "0px";
        });
        motionTimer = setTimeout(() => finish(), 700);
      });
    });
  }

  return {
    saveTreeState,
    confirmAction,
    formatDate,
    showNotice,
    bindAdminTheme,
    bindAdminTreeGroup,
  };
}
