/* Shared value update used by the administrative panel. */
window.motionValue = (element, value) => {
  if (!element || element.textContent === String(value)) return;
  element.textContent = String(value);
  if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
  element.classList.remove('value-change');
  void element.offsetWidth;
  element.classList.add('value-change');
  element.addEventListener('animationend', () => element.classList.remove('value-change'), {once: true});
};
