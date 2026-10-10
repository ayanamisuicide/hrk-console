// Пользователи, роли, сессии и одноразовые приглашения.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createUsers(ctx) {
  // Рисует пользователей, присутствие и управление ролями; неизменившийся список сохраняет DOM.
  function renderUsers(users) {
    const signature = JSON.stringify(users);
    if (signature === ctx.usersSignature) return;
    ctx.usersSignature = signature;
    const body = ctx.$("#users-body");
    body.replaceChildren();
    ctx.$("#users-empty").hidden = users.length !== 0;
    for (const user of users) {
      const row = document.createElement("tr");
      const name = document.createElement("td");
      name.dataset.label = "Пользователь";
      name.innerHTML = `<strong></strong><small></small>`;
      name.querySelector("strong").textContent = user.username;
      name.querySelector("small").textContent = ctx.sessionLabel(
        user.activeSessions,
      );

      const roleCell = document.createElement("td");
      roleCell.dataset.label = "Роль";
      const role = document.createElement("select");
      role.className = "role-select";
      for (const [value, label] of [
        ["operator", "Оператор"],
        ["viewer", "Наблюдатель"],
      ]) {
        const option = document.createElement("option");
        option.value = value;
        option.textContent = label;
        role.append(option);
      }
      role.value = user.role || "operator";
      role.addEventListener("change", async () => {
        role.disabled = true;
        try {
          await ctx.adminRequest(
            `/api/admin/users/${encodeURIComponent(user.username)}/role`,
            { method: "PATCH", body: JSON.stringify({ role: role.value }) },
          );
          ctx.showNotice(`Роль ${user.username} изменена`);
          await ctx.refresh();
        } catch (error) {
          role.value = user.role || "operator";
          ctx.showNotice(error.message, "error");
        }
        role.disabled = false;
      });
      roleCell.append(role);

      const status = document.createElement("td");
      status.dataset.label = "Статус";
      const badge = document.createElement("span");
      badge.className = `presence ${user.online ? "online" : ""}`;
      badge.textContent = user.online ? "● онлайн" : "○ офлайн";
      status.append(badge);

      const created = document.createElement("td");
      created.dataset.label = "Регистрация";
      created.textContent = ctx.formatDate(user.createdAt);
      const seen = document.createElement("td");
      seen.dataset.label = "Активность";
      seen.textContent = ctx.formatDate(user.lastSeen);
      const actions = document.createElement("td");
      actions.dataset.label = "Действия";
      const remove = document.createElement("button");
      remove.className = "danger compact";
      remove.textContent = "Удалить";
      remove.setAttribute(
        "aria-label",
        `Удалить пользователя ${user.username}`,
      );
      remove.addEventListener("click", () => ctx.deleteUser(user.username));
      actions.append(remove);
      row.append(name, roleCell, status, created, seen, actions);
      body.append(row);
    }
  }

  // Рисует действующие приглашения и кнопки копирования и отзыва.
  function renderInvites(invites) {
    const signature = JSON.stringify(invites);
    if (signature === ctx.invitesSignature) return;
    ctx.invitesSignature = signature;
    const grid = ctx.$("#invites-grid");
    grid.replaceChildren();
    ctx.$("#invites-empty").hidden = invites.length !== 0;
    for (const invite of invites) {
      const card = document.createElement("article");
      card.className = "invite-card";
      const token = document.createElement("code");
      token.textContent = invite.token;
      const expiry = document.createElement("p");
      expiry.textContent = `${invite.role === "viewer" ? "Наблюдатель" : "Оператор"} · действует до ${ctx.formatDate(invite.expiresAt)}`;
      const actions = document.createElement("div");
      const copy = document.createElement("button");
      copy.className = "compact";
      copy.textContent = "Копировать ссылку";
      copy.addEventListener("click", async () => {
        await navigator.clipboard.writeText(invite.registrationUrl);
        ctx.showNotice("Ссылка регистрации скопирована");
      });
      const revoke = document.createElement("button");
      revoke.className = "danger compact";
      revoke.textContent = "Отозвать";
      revoke.addEventListener("click", () => ctx.revokeInvite(invite.token));
      actions.append(copy, revoke);
      card.append(token, expiry, actions);
      grid.append(card);
    }
  }

  // После подтверждения удаляет пользователя и обновляет списки.
  async function deleteUser(username) {
    if (
      !(await ctx.confirmAction(
        "Удалить пользователя?",
        `${username} потеряет доступ, а все активные сессии завершатся.`,
      ))
    )
      return;
    try {
      await ctx.adminRequest(
        `/api/admin/users/${encodeURIComponent(username)}`,
        { method: "DELETE" },
      );
      ctx.showNotice(`Пользователь ${username} удалён`);
      await ctx.refresh();
    } catch (error) {
      ctx.showNotice(error.message, "error");
    }
  }

  // После подтверждения отзывает приглашение и обновляет списки.
  async function revokeInvite(token) {
    if (
      !(await ctx.confirmAction(
        "Отозвать инвайт?",
        "Ссылка регистрации сразу перестанет работать.",
      ))
    )
      return;
    try {
      await ctx.adminRequest(
        `/api/admin/invites/${encodeURIComponent(token)}`,
        { method: "DELETE" },
      );
      ctx.showNotice("Инвайт отозван");
      await ctx.refresh();
    } catch (error) {
      ctx.showNotice(error.message, "error");
    }
  }

  // Выбирает русскую форму количества сессий.
  function sessionLabel(count) {
    const mod100 = count % 100;
    const mod10 = count % 10;
    if (mod100 >= 11 && mod100 <= 14) return `${count} активных сессий`;
    if (mod10 === 1) return `${count} активная сессия`;
    if (mod10 >= 2 && mod10 <= 4) return `${count} активные сессии`;
    return `${count} активных сессий`;
  }
  // Создаёт приглашение выбранной роли и показывает ссылку для регистрации.
  function bindInviteForm() {
    ctx.$("#invite-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      const expiresHours = Number(ctx.$("#invite-hours").value);
      const role = ctx.$("#invite-role").value;
      try {
        const invite = await ctx.adminRequest("/api/admin/invites", {
          method: "POST",
          body: JSON.stringify({ expiresHours, role }),
        });
        ctx.showNotice(`Инвайт создан до ${ctx.formatDate(invite.expiresAt)}`);
        await ctx.refresh();
      } catch (error) {
        ctx.showNotice(error.message, "error");
      }
    });
  }

  return {
    renderUsers,
    renderInvites,
    deleteUser,
    revokeInvite,
    sessionLabel,
    bindInviteForm,
  };
}
