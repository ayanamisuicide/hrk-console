// Старое окно обновления: открываем раздел «Обновления» в админке.
try {
  localStorage.setItem("hkc-admin-view", "updates");
} catch (_) {
  /* Без хранилища админка откроется на первом разделе. */
}
location.replace("/admin/");
