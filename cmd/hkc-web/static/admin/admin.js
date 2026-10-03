const $ = (selector) => document.querySelector(selector);
const authDialog = $('#admin-auth');
function animateValue(element, value) {
  if (window.motionValue) { window.motionValue(element, value); return; }
  const next = String(value);
  if (!element || element.textContent === next) return;
  element.textContent = next;
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;
  element.classList.remove('value-change');
  void element.offsetWidth;
  element.classList.add('value-change');
  element.addEventListener('animationend', () => element.classList.remove('value-change'), {once: true});
}
document.documentElement.dataset.theme = localStorage.getItem('hkc-theme') || 'dark';
$('#admin-theme').addEventListener('click', () => {
  const theme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  window.motionTheme?.();
  document.documentElement.dataset.theme = theme;
  localStorage.setItem('hkc-theme', theme);
});
let adminToken = sessionStorage.getItem('hkc-admin-token') || '';
let refreshTimer;
let usersSignature = '';
let invitesSignature = '';
let backupsSignature = '';
try {
  const openBranches = JSON.parse(localStorage.getItem('hkc-admin-tree') || 'null');
  if (Array.isArray(openBranches)) document.querySelectorAll('.admin-tree-group').forEach((branch) => { branch.open = openBranches.includes(branch.dataset.tree); });
} catch (_) { localStorage.removeItem('hkc-admin-tree'); }
function saveTreeState() {
  const open = [...document.querySelectorAll('.admin-tree-group[open]')].map((item) => item.dataset.tree);
  localStorage.setItem('hkc-admin-tree', JSON.stringify(open));
}
document.querySelectorAll('.admin-tree-group').forEach((branch) => {
  const summary = branch.querySelector(':scope > summary');
  const clip = branch.querySelector(':scope > .admin-tree-clip');
  let frame = 0;
  let motionTimer;
  let motionEnd;
  const cancelMotion = () => {
    cancelAnimationFrame(frame);
    clearTimeout(motionTimer);
    if (motionEnd && clip) clip.removeEventListener('transitionend', motionEnd);
    motionEnd = null;
  };
  summary.setAttribute('aria-expanded', String(branch.open));
  summary.addEventListener('click', (event) => {
    event.preventDefault();
    const visuallyOpen = branch.open && !branch.classList.contains('is-closing');
    const open = !visuallyOpen;
    cancelMotion();
    if (open) {
      const startHeight = branch.open && clip ? clip.getBoundingClientRect().height : 0;
      if (clip) clip.style.height = `${startHeight}px`;
      branch.classList.remove('is-closing');
      if (clip) clip.inert = false;
      branch.open = true;
      summary.setAttribute('aria-expanded', 'true');
      if (clip && !window.prefersReducedMotion?.()) {
        void clip.offsetHeight;
        frame = requestAnimationFrame(() => { clip.style.height = `${clip.scrollHeight}px`; });
        const finish = (transitionEvent) => {
          if (transitionEvent && (transitionEvent.target !== clip || transitionEvent.propertyName !== 'height')) return;
          cancelMotion();
          clip.style.height = '';
        };
        motionEnd = finish;
        clip.addEventListener('transitionend', finish);
        motionTimer = setTimeout(() => finish(), 700);
      } else if (clip) clip.style.height = '';
      saveTreeState();
      return;
    }
    summary.setAttribute('aria-expanded', 'false');
    if (window.prefersReducedMotion?.() || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches || !branch.open || !clip) {
      branch.open = false;
      if (clip) { clip.inert = true; clip.style.height = ''; }
      branch.classList.remove('is-closing');
      saveTreeState();
      return;
    }
    clip.inert = true;
    clip.style.height = `${clip.getBoundingClientRect().height}px`;
    void clip.offsetHeight;
    branch.classList.add('is-closing');
    const finish = (transitionEvent) => {
      if (transitionEvent && (transitionEvent.target !== clip || transitionEvent.propertyName !== 'height')) return;
      if (!branch.classList.contains('is-closing')) return;
      cancelMotion();
      branch.open = false;
      clip.style.height = '';
      branch.classList.remove('is-closing');
      saveTreeState();
    };
    motionEnd = finish;
    clip.addEventListener('transitionend', finish);
    frame = requestAnimationFrame(() => { clip.style.height = '0px'; });
    motionTimer = setTimeout(() => finish(), 700);
  });
});

function confirmAction(title, message) {
  const dialog = $('#confirm-dialog');
  $('#confirm-title').textContent = title;
  $('#confirm-message').textContent = message;
  dialog.showModal();
  return new Promise((resolve) => {
    dialog.addEventListener('close', () => resolve(dialog.returnValue === 'confirm'), {once: true});
  });
}

async function adminRequest(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set('Authorization', `Bearer ${adminToken}`);
  if (options.body) headers.set('Content-Type', 'application/json');
  const response = await fetch(path, {...options, headers});
  const body = await response.json();
  if (response.status === 401) {
    sessionStorage.removeItem('hkc-admin-token');
    adminToken = '';
    openAuth('Неверный или изменившийся административный токен');
    throw new Error(body.message || 'требуется административный токен');
  }
  if (!response.ok) throw new Error(body.message || `HTTP ${response.status}`);
  return body;
}

function openAuth(message = '') {
  $('#admin-auth-error').textContent = message;
  if (!authDialog.open) authDialog.showModal();
}

function formatDate(value) {
  if (!value) return 'никогда';
  return new Intl.DateTimeFormat('ru-RU', {dateStyle: 'medium', timeStyle: 'short'}).format(new Date(value));
}

function sessionLabel(count) {
  const mod100 = count % 100;
  const mod10 = count % 10;
  if (mod100 >= 11 && mod100 <= 14) return `${count} активных сессий`;
  if (mod10 === 1) return `${count} активная сессия`;
  if (mod10 >= 2 && mod10 <= 4) return `${count} активные сессии`;
  return `${count} активных сессий`;
}

function showNotice(message, kind = 'ok') {
  const node = $('#admin-notice');
  node.textContent = message;
  node.className = `notice ${kind}`;
  if (window.motionShow) { window.motionShow(node, 5000); return; }
  node.hidden = false;
  clearTimeout(showNotice.timer);
  showNotice.timer = setTimeout(() => { node.hidden = true; }, 5000);
}

function renderUsers(users) {
  const signature = JSON.stringify(users);
  if (signature === usersSignature) return;
  usersSignature = signature;
  const body = $('#users-body');
  body.replaceChildren();
  $('#users-empty').hidden = users.length !== 0;
  for (const user of users) {
    const row = document.createElement('tr');
    const name = document.createElement('td');
    name.dataset.label = 'Пользователь';
    name.innerHTML = `<strong></strong><small></small>`;
    name.querySelector('strong').textContent = user.username;
    name.querySelector('small').textContent = sessionLabel(user.activeSessions);

    const roleCell = document.createElement('td');
    roleCell.dataset.label = 'Роль';
    const role = document.createElement('select');
    role.className = 'role-select';
    for (const [value, label] of [['operator', 'Оператор'], ['viewer', 'Наблюдатель']]) {
      const option = document.createElement('option'); option.value = value; option.textContent = label; role.append(option);
    }
    role.value = user.role || 'operator';
    role.addEventListener('change', async () => {
      role.disabled = true;
      try {
        await adminRequest(`/api/admin/users/${encodeURIComponent(user.username)}/role`, {method: 'PATCH', body: JSON.stringify({role: role.value})});
        showNotice(`Роль ${user.username} изменена`);
        await refresh();
      } catch (error) { role.value = user.role || 'operator'; showNotice(error.message, 'error'); }
      role.disabled = false;
    });
    roleCell.append(role);

    const status = document.createElement('td');
    status.dataset.label = 'Статус';
    const badge = document.createElement('span');
    badge.className = `presence ${user.online ? 'online' : ''}`;
    badge.textContent = user.online ? '● онлайн' : '○ офлайн';
    status.append(badge);

    const created = document.createElement('td');
    created.dataset.label = 'Регистрация';
    created.textContent = formatDate(user.createdAt);
    const seen = document.createElement('td');
    seen.dataset.label = 'Активность';
    seen.textContent = formatDate(user.lastSeen);
    const actions = document.createElement('td');
    actions.dataset.label = 'Действия';
    const remove = document.createElement('button');
    remove.className = 'danger compact';
    remove.textContent = 'Удалить';
    remove.setAttribute('aria-label', `Удалить пользователя ${user.username}`);
    remove.addEventListener('click', () => deleteUser(user.username));
    actions.append(remove);
    row.append(name, roleCell, status, created, seen, actions);
    body.append(row);
  }
}

function renderInvites(invites) {
  const signature = JSON.stringify(invites);
  if (signature === invitesSignature) return;
  invitesSignature = signature;
  const grid = $('#invites-grid');
  grid.replaceChildren();
  $('#invites-empty').hidden = invites.length !== 0;
  for (const invite of invites) {
    const card = document.createElement('article');
    card.className = 'invite-card';
    const token = document.createElement('code');
    token.textContent = invite.token;
    const expiry = document.createElement('p');
    expiry.textContent = `${invite.role === 'viewer' ? 'Наблюдатель' : 'Оператор'} · действует до ${formatDate(invite.expiresAt)}`;
    const actions = document.createElement('div');
    const copy = document.createElement('button');
    copy.className = 'compact';
    copy.textContent = 'Копировать ссылку';
    copy.addEventListener('click', async () => {
      await navigator.clipboard.writeText(invite.registrationUrl);
      showNotice('Ссылка регистрации скопирована');
    });
    const revoke = document.createElement('button');
    revoke.className = 'danger compact';
    revoke.textContent = 'Отозвать';
    revoke.addEventListener('click', () => revokeInvite(invite.token));
    actions.append(copy, revoke);
    card.append(token, expiry, actions);
    grid.append(card);
  }
}

function renderBot(bot) {
  animateValue($('#admin-bot-status'), bot.running ? 'Heroku работает' : 'Heroku остановлен');
  $('#admin-bot-meta').textContent = bot.running ? `PID ${bot.pid} · ${bot.uptime} · версия ${bot.version || '—'}` : bot.herokuDir;
  $('#bot-orbit-dot').classList.toggle('online', bot.running);
  document.querySelector('[data-bot-action="start"]').disabled = bot.running;
  document.querySelector('[data-bot-action="restart"]').disabled = !bot.running;
  document.querySelector('[data-bot-action="stop"]').disabled = !bot.running;
}

let auditVisible = 12;
let auditSignature = '';
let auditEvents = [];
const auditOpen = new Set();
function auditGroups(events) {
  const groups = [];
  const byIdentity = new Map();
  for (const event of events) {
    const identity = JSON.stringify([event.actor, event.action, event.detail, event.ip]);
    if (byIdentity.has(identity)) byIdentity.get(identity).events.push(event);
    else { const group = {identity, events: [event]}; groups.push(group); byIdentity.set(identity, group); }
  }
  return groups;
}

function repetitionLabel(count) {
  const suffix = count % 100 >= 11 && count % 100 <= 14 ? 'раз' : count % 10 >= 2 && count % 10 <= 4 ? 'раза' : 'раз';
  return `${count} ${suffix}`;
}

function drawAudit() {
  const groups = auditGroups(auditEvents);
  const list = $('#audit-groups'); list.replaceChildren();
  $('#audit-empty').hidden = groups.length !== 0;
  groups.slice(0, auditVisible).forEach((group, index) => {
    const event = group.events[0];
    const key = group.identity;
    const card = document.createElement('article'); card.className = 'audit-group';
    const head = document.createElement('div'); head.className = 'audit-group-head';
    const action = document.createElement('strong'); action.textContent = event.action || 'Действие';
    const detail = document.createElement('p'); detail.textContent = event.detail || 'Без описания';
    const meta = document.createElement('span'); meta.textContent = `${event.actor || '—'} · ${formatDate(event.time)} · ${event.ip || '—'}`;
    const main = document.createElement('div'); main.append(action, detail, meta); head.append(main);
    if (group.events.length > 1) {
      const button = document.createElement('button'); button.className = 'compact audit-toggle';
      const panel = document.createElement('div'); panel.className = 'audit-expander'; panel.id = `audit-detail-${index}`;
      const inner = document.createElement('div'); inner.className = 'audit-expander-inner';
      for (const occurrence of group.events) {
        const row = document.createElement('div'); row.className = 'audit-occurrence';
        const time = document.createElement('time'); time.textContent = formatDate(occurrence.time);
        const copy = document.createElement('span'); copy.textContent = `${occurrence.actor || '—'} · ${occurrence.ip || '—'}`;
        row.append(time, copy); inner.append(row);
      }
      panel.append(inner);
      const toggle = (open) => {
        card.classList.toggle('open', open); button.setAttribute('aria-expanded', String(open));
        button.textContent = `${repetitionLabel(group.events.length)} ${open ? '▴' : '▾'}`;
        panel.inert = !open;
        panel.setAttribute('aria-hidden', String(!open));
        if (open) auditOpen.add(key); else auditOpen.delete(key);
      };
      button.setAttribute('aria-controls', panel.id);
      button.addEventListener('click', () => toggle(!card.classList.contains('open')));
      head.append(button); card.append(head, panel); toggle(auditOpen.has(key));
    } else card.append(head);
    list.append(card);
  });
  $('#audit-more').hidden = auditVisible >= groups.length;
  $('#audit-more').textContent = `Показать ещё · ${Math.min(12, groups.length - auditVisible)}`;
}

function renderAudit(events) {
  const signature = JSON.stringify(events);
  if (signature === auditSignature) return;
  auditSignature = signature;
  auditEvents = events;
  drawAudit();
}
$('#audit-more').addEventListener('click', () => { auditVisible += 12; drawAudit(); });

function renderBackups(backups) {
  const signature = JSON.stringify(backups);
  if (signature === backupsSignature) return;
  backupsSignature = signature;
  const list = $('#backup-list'); list.replaceChildren();
  $('#backup-empty').hidden = backups.length !== 0;
  for (const backup of backups) {
    const row = document.createElement('div'); row.className = 'backup-row';
    const info = document.createElement('div');
    const title = document.createElement('strong'); title.textContent = formatDate(backup.createdAt);
    const detail = document.createElement('small'); detail.textContent = `${backup.name} · ${(backup.size / 1024).toFixed(1)} КБ`;
    info.append(title, detail);
    const restore = document.createElement('button'); restore.className = 'compact'; restore.textContent = 'Восстановить';
    restore.addEventListener('click', async () => {
      if (!await confirmAction('Восстановить базу доступа?', `Будут восстановлены пользователи и инвайты из копии ${backup.name}. Все пользовательские сессии завершатся.`)) return;
      try {
        const result = await adminRequest(`/api/admin/backups/${encodeURIComponent(backup.name)}/restore`, {method: 'POST'});
        showNotice(result.message); await refresh();
      } catch (error) { showNotice(error.message, 'error'); }
    });
    row.append(info, restore); list.append(row);
  }
}

async function refresh() {
  if (!adminToken) {
    openAuth();
    return;
  }
  try {
    const [data, audit, backups] = await Promise.all([adminRequest('/api/admin/overview'), adminRequest('/api/admin/audit'), adminRequest('/api/admin/backups')]);
    animateValue($('#users-count'), data.users.length);
    animateValue($('#online-count'), data.users.filter((user) => user.online).length);
    animateValue($('#invites-count'), data.invites.length);
    renderBot(data.bot);
    renderUsers(data.users);
    renderInvites(data.invites);
    renderAudit(audit.events || []);
    renderBackups(backups.backups || []);
    await refreshUpdates();
  } catch (error) {
    if (adminToken) showNotice(error.message, 'error');
  }
}

document.querySelectorAll('[data-bot-action]').forEach((button) => {
  button.addEventListener('click', async () => {
    const action = button.dataset.botAction;
    button.disabled = true;
    try {
      const result = await adminRequest(`/api/admin/bot/${action}`, {method: 'POST'});
      showNotice(result.message);
      setTimeout(refresh, 500);
    } catch (error) {
      showNotice(error.message, 'error');
      await refresh();
    }
  });
});

async function deleteUser(username) {
  if (!await confirmAction('Удалить пользователя?', `${username} потеряет доступ, а все активные сессии завершатся.`)) return;
  try {
    await adminRequest(`/api/admin/users/${encodeURIComponent(username)}`, {method: 'DELETE'});
    showNotice(`Пользователь ${username} удалён`);
    await refresh();
  } catch (error) {
    showNotice(error.message, 'error');
  }
}

async function revokeInvite(token) {
  if (!await confirmAction('Отозвать инвайт?', 'Ссылка регистрации сразу перестанет работать.')) return;
  try {
    await adminRequest(`/api/admin/invites/${encodeURIComponent(token)}`, {method: 'DELETE'});
    showNotice('Инвайт отозван');
    await refresh();
  } catch (error) {
    showNotice(error.message, 'error');
  }
}

$('#admin-auth-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  adminToken = $('#admin-token').value.trim();
  try {
    await adminRequest('/api/admin/overview');
    sessionStorage.setItem('hkc-admin-token', adminToken);
    authDialog.close();
    await refresh();
  } catch (error) {
    $('#admin-auth-error').textContent = error.message;
  }
});

$('#invite-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const expiresHours = Number($('#invite-hours').value);
  const role = $('#invite-role').value;
  try {
    const invite = await adminRequest('/api/admin/invites', {
      method: 'POST', body: JSON.stringify({expiresHours, role}),
    });
    showNotice(`Инвайт создан до ${formatDate(invite.expiresAt)}`);
    await refresh();
  } catch (error) {
    showNotice(error.message, 'error');
  }
});

$('#admin-logout').addEventListener('click', () => {
  sessionStorage.removeItem('hkc-admin-token');
  adminToken = '';
  openAuth();
});

$('#admin-refresh').addEventListener('click', async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  button.textContent = 'Обновление…';
  await refresh();
  button.textContent = '↻ Обновить';
  button.disabled = false;
});

$('#create-backup').addEventListener('click', async (event) => {
  const button = event.currentTarget; button.disabled = true;
  try { await adminRequest('/api/admin/backups', {method: 'POST'}); showNotice('Резервная копия создана'); await refresh(); }
  catch (error) { showNotice(error.message, 'error'); }
  button.disabled = false;
});

document.querySelectorAll('[data-diagnostic]').forEach((button) => button.addEventListener('click', async () => {
  button.disabled = true; $('#diagnostic-output').textContent = 'Выполняется проверка…';
  try {
    const result = await adminRequest(`/api/admin/diagnostics/${button.dataset.diagnostic}`, {method: 'POST'});
    $('#diagnostic-output').textContent = result.output || 'Команда не вернула данные.';
  } catch (error) { $('#diagnostic-output').textContent = error.message; }
  button.disabled = false;
}));

const terminalForm = $('#terminal-form');
const terminalCommand = $('#terminal-command');
const terminalOutput = $('#terminal-output');
const terminalMeta = $('#terminal-meta');
terminalForm.addEventListener('submit', async (event) => {
  event.preventDefault();
  const command = terminalCommand.value.trim();
  if (!command) { terminalCommand.focus(); return; }
  const potentiallyDangerous = /(?:^|[\s;&|])(rm|sudo|chmod|chown|dd|mkfs|reboot|shutdown|kill|pkill|truncate|git\s+reset|git\s+clean)(?:\s|$)/i.test(command);
  const warning = potentiallyDangerous ? '⚠ Команда похожа на потенциально опасную. Проверьте её особенно внимательно. ' : '';
  if (!await confirmAction('Выполнить команду в WSL?', `${warning}Команда будет запущена с правами службы панели в каталоге Heroku: ${command}`)) return;
  const button = $('#terminal-run');
  button.disabled = true;
  terminalCommand.disabled = true;
  terminalMeta.textContent = 'Выполняется…';
  terminalOutput.textContent = `$ ${command}\n`;
  try {
    const result = await adminRequest('/api/admin/terminal', {method: 'POST', body: JSON.stringify({command, confirmed: true})});
    terminalOutput.textContent += result.output || '(команда не вернула вывод)';
    terminalMeta.textContent = `${result.actor || 'администратор'} · exit ${result.exitCode} · ${result.durationMs} мс${result.timedOut ? ' · таймаут' : ''}`;
  } catch (error) {
    terminalOutput.textContent += error.message;
    terminalMeta.textContent = 'Ошибка выполнения';
  }
  terminalCommand.disabled = false;
  button.disabled = false;
  terminalCommand.focus();
});
terminalCommand.addEventListener('keydown', (event) => {
  if (event.key === 'Enter' && event.ctrlKey) { event.preventDefault(); terminalForm.requestSubmit(); }
});
$('#terminal-clear').addEventListener('click', () => {
  terminalOutput.textContent = 'Вывод очищен.';
  terminalMeta.textContent = 'Готово к команде';
});

async function refreshUpdates() {
  try {
    const data = await adminRequest('/api/admin/updates');
    const short = value => value ? value.slice(0, 12) : 'неизвестен';
    const installed = data.installed;
    $('#update-installed').textContent = installed.version + (installed.modified ? ' · изменена' : '');
    $('#update-installed-commit').textContent = short(installed.commit);
    for (const key of ['source', 'local']) {
      const copy = data[key];
      $('#update-' + key).textContent = !copy.configured ? 'Не подключена' : copy.error ? 'Ошибка доступа' : copy.dirty ? 'Есть свои изменения' : copy.commit === installed.commit ? 'Совпадает со сборкой' : 'Отличается от сборки';
      $('#update-' + key + '-commit').textContent = copy.error || (short(copy.commit) + (copy.branch ? ' / ' + copy.branch : ''));
    }
    const remote = data.github;
    $('#update-release').textContent = remote.version || (remote.checking ? 'Проверяем…' : 'Нет данных');
    $('#update-release-commit').textContent = short(remote.commit);
    $('#update-main').textContent = 'GitHub main: ' + short(remote.main) + (remote.checkedAt && !remote.checkedAt.startsWith('0001') ? ' · Сверка: ' + formatDate(remote.checkedAt) : '');
    const blocked = [data.source, data.local].some(copy => copy.configured && (copy.dirty || copy.error || copy.branch !== 'main'));
    const busy = ['checking', 'downloading', 'restarting'].includes(data.job?.phase);
    let message = remote.error ? 'Сверка с GitHub не выполнена: ' + remote.error : remote.checking ? 'Сверяем GitHub…' : remote.commit === installed.commit ? 'Сборка совпадает со стабильным релизом GitHub.' : remote.commit ? 'На GitHub доступен другой стабильный релиз: ' + remote.version + '.' : 'Ожидаем результат сверки.';
    if (blocked) message += ' Обновление заблокировано: сначала сохраните локальные изменения и выберите main.';
    if (!data.enabled) message += ' Служба установки на этом сервере не настроена.';
    $('#update-summary').textContent = message;
    $('#update-job').textContent = data.job ? data.job.message + (data.job.updatedAt ? ' · ' + formatDate(data.job.updatedAt) : '') : '';
    const syncNeeded = [data.source, data.local].some(copy => copy.configured && copy.commit !== remote.commit);
    $('#updates-install').textContent = remote.commit === installed.commit && syncNeeded ? 'Открыть синхронизацию' : 'Открыть окно обновления';
    $('#updates-install').disabled = !data.enabled || blocked || busy || !!remote.error || remote.checking || !remote.commit || (remote.commit === installed.commit && !syncNeeded) || installed.modified;
  } catch (error) {
    $('#update-summary').textContent = 'Не удалось получить состояние обновлений: ' + error.message;
    $('#updates-install').disabled = true;
  }
}
$('#updates-check').addEventListener('click', async () => {
  $('#updates-check').disabled = true;
  try { await adminRequest('/api/admin/updates/check', {method: 'POST'}); await refreshUpdates(); }
  catch (error) { showNotice(error.message, 'error'); }
  finally { $('#updates-check').disabled = false; }
});
function openUpdateWindow() {
  const progress = window.open('/admin/update.html', 'hkc-update-progress', 'width=980,height=780');
  if (progress) progress.focus();
  else showNotice('Разрешите всплывающие окна для панели обновления', 'error');
}
$('#updates-install').addEventListener('click', openUpdateWindow);

refresh();
refreshTimer = setInterval(refresh, 10000);
