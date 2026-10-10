// Публичная сводка обращается к открытому API; на сервере она выключена по умолчанию.
fetch("/api/public/status")
  .then(async (response) => {
    if (!response.ok) throw new Error("Публичный статус не включён");
    return response.json();
  })
  .then((data) => {
    const card = document.querySelector("#public-state");
    card.classList.toggle("online", data.running);
    card.querySelector(".dot").classList.toggle("online", data.running);
    card.querySelector("strong").textContent = data.running
      ? "Сервис работает"
      : "Сервис остановлен";
    card.classList.add("is-ready");
    document.querySelector("#public-time").textContent =
      `Проверено ${new Date(data.checkedAt).toLocaleString("ru-RU")}`;
  })
  .catch((error) => {
    document.querySelector("#public-state strong").textContent = error.message;
    document.querySelector("#public-state").classList.add("is-ready");
  });
