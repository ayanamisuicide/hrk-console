(() => {
  const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  const revealTargets = document.querySelectorAll(
    '.metric-card, .surface-card, .admin-section, .summary article, .bot-deck',
  );

  window.motionValue = (element, value) => {
    const next = String(value);
    if (!element || element.textContent === next) return;
    element.textContent = next;
    if (reduced) return;
    element.classList.remove('value-change');
    void element.offsetWidth;
    element.classList.add('value-change');
    element.addEventListener('animationend', () => element.classList.remove('value-change'), {once: true});
  };

  if (!reduced && typeof window.IntersectionObserver === 'function') {
    const observer = new IntersectionObserver((entries) => {
      for (const entry of entries) {
        if (!entry.isIntersecting) continue;
        entry.target.dataset.reveal = 'shown';
        observer.unobserve(entry.target);
      }
    }, {threshold: 0.12, rootMargin: '0px 0px -28px 0px'});
    revealTargets.forEach((element) => {
      element.dataset.reveal = 'waiting';
      observer.observe(element);
    });
  } else if (!reduced) {
    document.documentElement.classList.add('motion-reveal-fallback');
  }

  if (!reduced) {
    document.addEventListener('pointermove', (event) => {
      const card = event.target.closest('.metric-card, .surface-card, .admin-section, .summary article, .invite-card');
      if (!card) return;
      const rect = card.getBoundingClientRect();
      card.style.setProperty('--pointer-x', `${event.clientX - rect.left}px`);
      card.style.setProperty('--pointer-y', `${event.clientY - rect.top}px`);
    }, {passive: true});

    document.addEventListener('pointerdown', (event) => {
      const button = event.target.closest('button:not(:disabled)');
      if (!button) return;
      const rect = button.getBoundingClientRect();
      button.style.setProperty('--ripple-x', `${event.clientX - rect.left}px`);
      button.style.setProperty('--ripple-y', `${event.clientY - rect.top}px`);
      button.classList.remove('is-rippling');
      void button.offsetWidth;
      button.classList.add('is-rippling');
      button.addEventListener('animationend', () => button.classList.remove('is-rippling'), {once: true});
    });
  }

})();
