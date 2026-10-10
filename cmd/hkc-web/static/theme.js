// Тема подключается в <head> без defer: атрибут ставится до первой отрисовки, чтобы не мигал фон.
// Выбор хранится в localStorage; без него берётся системная настройка.
(() => {
  const root = document.documentElement;
  const read = () => {
    try {
      return localStorage.getItem("hkc-theme");
    } catch {
      return null;
    }
  };
  const system = () =>
    window.matchMedia?.("(prefers-color-scheme: light)").matches ? "light" : "dark";
  const apply = (theme) => {
    root.dataset.theme = theme === "light" ? "light" : "dark";
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.content = theme === "light" ? "#f6f7f9" : "#0a0b0e";
    const next = theme === "light" ? "Тёмная тема" : "Светлая тема";
    const label = document.querySelector("#theme-toggle span");
    if (label) label.textContent = next;
    const admin = document.querySelector("#admin-theme span");
    if (admin) admin.textContent = theme === "light" ? "Тёмная" : "Светлая";
  };
  apply(read() || system());

  // Плавная смена цветов только в момент переключения, чтобы не замедлять обычную работу.
  window.toggleTheme = () => {
    const theme = root.dataset.theme === "light" ? "dark" : "light";
    root.classList.add("theme-switching");
    apply(theme);
    try {
      localStorage.setItem("hkc-theme", theme);
    } catch {
      /* без хранилища тема живёт до перезагрузки */
    }
    setTimeout(() => root.classList.remove("theme-switching"), 320);
  };

  document.addEventListener("DOMContentLoaded", () => {
    apply(root.dataset.theme);
    for (const id of ["#theme-toggle", "#admin-theme"])
      document.querySelector(id)?.addEventListener("click", window.toggleTheme);
  });
})();
