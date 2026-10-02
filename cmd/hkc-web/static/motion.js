(() => {
  const motionPreference = window.matchMedia?.('(prefers-reduced-motion: reduce)');
  const reducedMotion = () => !!motionPreference?.matches;
  const revealTargets = document.querySelectorAll(
    '.metric-card, .surface-card, .admin-section, .summary article, .bot-deck, .update-versions, .update-timeline',
  );
  const lastValueMotion = new WeakMap();

  window.prefersReducedMotion = reducedMotion;
  window.motionTheme = () => {
    if (reducedMotion()) return;
    const root = document.documentElement;
    clearTimeout(root.motionThemeTimer);
    root.classList.add('theme-transition');
    root.motionThemeTimer = setTimeout(() => root.classList.remove('theme-transition'), 480);
  };
  window.motionValue = (element, value) => {
    const next = String(value);
    if (!element || element.textContent === next) return;
    const previous = element.textContent.trim();
    element.textContent = next;
    if (reducedMotion() || document.hidden || ['—', '-', '', '0'].includes(previous)) return;

    const now = performance.now();
    if (lastValueMotion.has(element) && now - lastValueMotion.get(element) < 440) return;
    lastValueMotion.set(element, now);
    element.classList.remove('value-change');
    void element.offsetWidth;
    element.classList.add('value-change');
    element.addEventListener('animationend', () => element.classList.remove('value-change'), {once: true});
  };

  window.motionShow = (element, duration = 5000) => {
    if (!element) return;
    clearTimeout(element.motionDismissTimer);
    clearTimeout(element.motionHideTimer);
    element.classList.remove('is-leaving');
    element.hidden = false;
    if (duration < 0) return;
    element.motionDismissTimer = setTimeout(() => {
      if (reducedMotion()) { element.hidden = true; return; }
      element.classList.add('is-leaving');
      element.motionHideTimer = setTimeout(() => {
        element.hidden = true;
        element.classList.remove('is-leaving');
      }, 300);
    }, duration);
  };

  document.querySelectorAll('.log-advanced').forEach((disclosure) => {
    const summary = disclosure.querySelector(':scope > summary');
    const panel = disclosure.querySelector(':scope > .log-advanced-grid');
    if (!summary || !panel) return;
    summary.setAttribute('aria-expanded', String(disclosure.open));
    summary.addEventListener('click', (event) => {
      event.preventDefault();
      const wasOpen = disclosure.open && !disclosure.classList.contains('is-closing');
      const open = !wasOpen;
      clearTimeout(disclosure.motionCloseTimer);
      summary.setAttribute('aria-expanded', String(open));
      if (reducedMotion()) {
        disclosure.open = open;
        panel.inert = !open;
        disclosure.classList.toggle('is-expanded', open);
        disclosure.classList.remove('is-closing');
        return;
      }
      if (open) {
        panel.inert = false;
        if (disclosure.open) {
          disclosure.classList.remove('is-closing');
          disclosure.classList.add('is-expanded');
        } else {
          disclosure.open = true;
          requestAnimationFrame(() => disclosure.classList.add('is-expanded'));
        }
        return;
      }
      panel.inert = true;
      disclosure.classList.remove('is-expanded');
      disclosure.classList.add('is-closing');
      disclosure.motionCloseTimer = setTimeout(() => {
        if (!disclosure.classList.contains('is-closing')) return;
        disclosure.open = false;
        disclosure.classList.remove('is-closing');
      }, 360);
    });
  });

  if (!reducedMotion() && typeof window.IntersectionObserver === 'function') {
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.dataset.reveal = 'shown';
        observer.unobserve(entry.target);
      }
    }, {threshold: 0.08, rootMargin: '0px 0px -20px 0px'});
    revealTargets.forEach((element) => {
      element.dataset.reveal = 'waiting';
      observer.observe(element);
    });

    document.addEventListener('pointermove', (event) => {
      const card = event.target.closest?.('.metric-card, .surface-card, .admin-section, .summary article, .bot-deck, .invite-card');
      if (!card) return;
      const rect = card.getBoundingClientRect();
      card.style.setProperty('--pointer-x', `${event.clientX - rect.left}px`);
      card.style.setProperty('--pointer-y', `${event.clientY - rect.top}px`);
    }, {passive: true});

    document.addEventListener('pointerdown', (event) => {
      const button = event.target.closest?.('button:not(:disabled)');
      if (!button) return;
      const rect = button.getBoundingClientRect();
      button.style.setProperty('--ripple-x', `${event.clientX - rect.left}px`);
      button.style.setProperty('--ripple-y', `${event.clientY - rect.top}px`);
      button.classList.remove('is-rippling');
      void button.offsetWidth;
      button.classList.add('is-rippling');
      button.addEventListener('animationend', () => button.classList.remove('is-rippling'), {once: true});
    });

    const handleMotionPreference = (event) => {
      if (!event.matches) return;
      observer.disconnect();
      revealTargets.forEach((element) => {
        if (element.dataset.reveal === 'waiting') element.dataset.reveal = 'shown';
      });
    };
    if (motionPreference?.addEventListener) motionPreference.addEventListener('change', handleMotionPreference);
    else motionPreference?.addListener?.(handleMotionPreference);
  } else if (!reducedMotion()) {
    document.documentElement.classList.add('motion-reveal-fallback');
  }
})();
