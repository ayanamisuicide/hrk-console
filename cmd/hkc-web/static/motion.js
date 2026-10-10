// Общие анимации подключаются до модулей страниц и публикуют только помощники window.motion*.
(() => {
  // Уведомление использует безопасный текст и общий таймер скрытия.
  window.motionNotice = (element, message, kind = "ok") => {
    if (!element) return;
    element.textContent = message;
    element.className = `notice ${kind}`;
    window.motionShow(element, 5000);
  };
  // Системная настройка уменьшенного движения имеет приоритет над декоративными эффектами.
  const motionPreference = window.matchMedia?.(
    "(prefers-reduced-motion: reduce)",
  );
  const reducedMotion = () => !!motionPreference?.matches;
  const revealTargets = document.querySelectorAll(
    ".metric-card, .surface-card, .admin-section, .summary article, .bot-deck, .update-versions, .update-timeline",
  );
  const lastValueMotion = new WeakMap();

  window.prefersReducedMotion = reducedMotion;
  // Один живой световой проход при входе, без блокирующей заставки и фиктивных процентов.
  if (!reducedMotion()) {
    const arrival = document.createElement("div");
    arrival.className = "shell-arrival";
    arrival.setAttribute("aria-hidden", "true");
    document.body.append(arrival);
    arrival.addEventListener("animationend", () => arrival.remove(), {
      once: true,
    });
    setTimeout(() => arrival.remove(), 2000);
  }
  // Реальная высота details анимируется в обе стороны; повторный клик меняет направление.
  window.motionDisclosure = (element, panel) => {
    const summary = element.querySelector("summary");
    let animation,
      target = element.open;
    summary.setAttribute("aria-expanded", String(target));
    summary.addEventListener("click", (event) => {
      event.preventDefault();
      const from = element.getBoundingClientRect().height;
      animation?.cancel();
      target = !target;
      summary.setAttribute("aria-expanded", String(target));
      panel.inert = !target;
      if (reducedMotion()) {
        element.open = target;
        element.style.height = "";
        element.style.overflow = "";
        return;
      }
      element.style.height = "";
      element.open = true;
      const to = target
        ? element.getBoundingClientRect().height
        : summary.getBoundingClientRect().height + 2;
      element.style.overflow = "hidden";
      animation = element.animate(
        [{ height: from + "px" }, { height: to + "px" }],
        { duration: 460, easing: "cubic-bezier(.16,1,.3,1)" },
      );
      animation.onfinish = () => {
        element.open = target;
        element.style.height = "";
        element.style.overflow = "";
        animation = undefined;
      };
      if (target)
        panel.animate(
          [
            { opacity: 0, transform: "translateY(-6px)" },
            { opacity: 1, transform: "none" },
          ],
          { duration: 380, easing: "ease-out" },
        );
    });
  };
  window.motionTheme = () => {
    if (reducedMotion()) return;
    const root = document.documentElement;
    clearTimeout(root.motionThemeTimer);
    root.classList.add("theme-transition");
    root.motionThemeTimer = setTimeout(
      () => root.classList.remove("theme-transition"),
      700,
    );
  };
  // Выделяем только реальное изменение текста; частые замеры ограничены по частоте анимации.
  window.motionValue = (element, value) => {
    const next = String(value);
    if (!element || element.textContent === next) return;
    const previous = element.textContent.trim();
    element.textContent = next;
    if (
      reducedMotion() ||
      document.hidden ||
      ["—", "-", "", "0"].includes(previous)
    )
      return;

    const now = performance.now();
    if (
      lastValueMotion.has(element) &&
      now - lastValueMotion.get(element) < 640
    )
      return;
    lastValueMotion.set(element, now);
    element.classList.remove("value-change");
    // Чтение геометрии отделяет снятие класса от повторного добавления и перезапускает CSS-анимацию.
    void element.offsetWidth;
    element.classList.add("value-change");
    element.addEventListener(
      "animationend",
      () => element.classList.remove("value-change"),
      { once: true },
    );
  };

  // Повторное сообщение отменяет прежние таймеры, чтобы старый таймер не спрятал новое.
  window.motionShow = (element, duration = 5000) => {
    if (!element) return;
    clearTimeout(element.motionDismissTimer);
    clearTimeout(element.motionHideTimer);
    element.classList.remove("is-leaving");
    element.hidden = false;
    if (duration < 0) return;
    element.motionDismissTimer = setTimeout(
      // Общие анимации подключаются до модулей страниц и публикуют только помощники window.motion*.
      () => {
        if (reducedMotion()) {
          element.hidden = true;
          return;
        }
        element.classList.add("is-leaving");
        element.motionHideTimer = setTimeout(
          // Общие анимации подключаются до модулей страниц и публикуют только помощники window.motion*.
          () => {
            element.hidden = true;
            element.classList.remove("is-leaving");
          },
          480,
        );
      },
      duration,
    );
  };

  // Нативный details закрывается после анимации; inert сразу исключает скрываемые поля из фокуса.
  document.querySelectorAll(".log-advanced").forEach((disclosure) => {
    const summary = disclosure.querySelector(":scope > summary");
    const panel = disclosure.querySelector(":scope > .log-advanced-grid");
    if (!summary || !panel) return;
    summary.setAttribute("aria-expanded", String(disclosure.open));
    summary.addEventListener("click", (event) => {
      event.preventDefault();
      const wasOpen =
        disclosure.open && !disclosure.classList.contains("is-closing");
      const open = !wasOpen;
      clearTimeout(disclosure.motionCloseTimer);
      summary.setAttribute("aria-expanded", String(open));
      if (reducedMotion()) {
        disclosure.open = open;
        panel.inert = !open;
        disclosure.classList.toggle("is-expanded", open);
        disclosure.classList.remove("is-closing");
        return;
      }
      if (open) {
        panel.inert = false;
        if (disclosure.open) {
          disclosure.classList.remove("is-closing");
          disclosure.classList.add("is-expanded");
        } else {
          disclosure.open = true;
          requestAnimationFrame(() => disclosure.classList.add("is-expanded"));
        }
        return;
      }
      panel.inert = true;
      disclosure.classList.remove("is-expanded");
      disclosure.classList.add("is-closing");
      disclosure.motionCloseTimer = setTimeout(
        // Общие анимации подключаются до модулей страниц и публикуют только помощники window.motion*.
        () => {
          if (!disclosure.classList.contains("is-closing")) return;
          disclosure.open = false;
          disclosure.classList.remove("is-closing");
        },
        600,
      );
    });
  });

  if (!reducedMotion() && typeof window.IntersectionObserver === "function") {
    // Каждый блок появляется один раз: после показа перестаём наблюдать за ним.
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (!entry.isIntersecting) continue;
          entry.target.dataset.reveal = "shown";
          observer.unobserve(entry.target);
        }
      },
      { threshold: 0.08, rootMargin: "0px 0px -20px 0px" },
    );
    revealTargets.forEach((element) => {
      element.dataset.reveal = "waiting";
      observer.observe(element);
    });

    // Частые pointermove объединяются в один расчёт на кадр, без очереди устаревших координат.
    let pointerFrame = 0;
    let pointerEvent;
    document.addEventListener(
      "pointermove",
      (event) => {
        pointerEvent = event;
        if (pointerFrame) return;
        pointerFrame = requestAnimationFrame(
          // Общие анимации подключаются до модулей страниц и публикуют только помощники window.motion*.
          () => {
            pointerFrame = 0;
            const current = pointerEvent;
            const card = current?.target.closest?.(
              ".metric-card, .surface-card, .admin-section, .summary article, .bot-deck, .invite-card",
            );
            if (!card) return;
            const rect = card.getBoundingClientRect();
            card.style.setProperty(
              "--pointer-x",
              `${current.clientX - rect.left}px`,
            );
            card.style.setProperty(
              "--pointer-y",
              `${current.clientY - rect.top}px`,
            );
          },
        );
      },
      { passive: true },
    );

    document.addEventListener("pointerdown", (event) => {
      const button = event.target.closest?.("button:not(:disabled)");
      if (!button) return;
      const rect = button.getBoundingClientRect();
      button.style.setProperty("--ripple-x", `${event.clientX - rect.left}px`);
      button.style.setProperty("--ripple-y", `${event.clientY - rect.top}px`);
      button.classList.remove("is-rippling");
      void button.offsetWidth;
      button.classList.add("is-rippling");
      button.addEventListener(
        "animationend",
        () => button.classList.remove("is-rippling"),
        { once: true },
      );
    });

    // Изменение системной настройки не должно оставить блоки в невидимом состоянии waiting.
    const handleMotionPreference = (event) => {
      if (!event.matches) return;
      observer.disconnect();
      revealTargets.forEach((element) => {
        if (element.dataset.reveal === "waiting")
          element.dataset.reveal = "shown";
      });
    };
    if (motionPreference?.addEventListener)
      motionPreference.addEventListener("change", handleMotionPreference);
    else motionPreference?.addListener?.(handleMotionPreference);
  } else if (!reducedMotion()) {
    document.documentElement.classList.add("motion-reveal-fallback");
  }
})();
