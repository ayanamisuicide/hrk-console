package main

import (
	"bufio"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

// startPrivateBus поднимает настоящий dbus-daemon на приватном адресе —
// не мок: сам протокол (регистрация, свойства, GetLayout) проверяется
// против реального демона, а не против самодельной заглушки, которая
// может расходиться с тем, что действительно ждёт D-Bus. Раньше трей не
// делали именно потому, что его нельзя было проверить на месте (см.
// CHANGELOG 1.8.0 и комментарий в tray.go) — здесь эта часть риска снята.
func startPrivateBus(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("dbus-daemon"); err != nil {
		t.Skip("dbus-daemon недоступен — пропускаю интеграционные тесты трея")
	}
	cmd := exec.Command("dbus-daemon", "--session", "--print-address", "--nofork")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("запуск dbus-daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	sc := bufio.NewScanner(stdout)
	if !sc.Scan() {
		t.Fatalf("dbus-daemon не напечатал адрес шины")
	}
	return strings.TrimSpace(sc.Text())
}

func connectTest(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatalf("подключение к тестовой шине: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// Без наблюдателя (StatusNotifierWatcher) на шине регистрация должна
// вернуть false, а не упасть паникой или зависнуть — десктоп без трея
// вовсе такой же обычный случай, как и десктоп с ним.
func TestRegisterStatusNotifierItemWithoutWatcherFails(t *testing.T) {
	addr := startPrivateBus(t)
	conn := connectTest(t, addr)

	ok := registerStatusNotifierItem(conn, func() {}, func() {})
	if ok {
		t.Error("регистрация без наблюдателя на шине не должна считаться успешной")
	}
}

type fakeWatcher struct {
	mu         sync.Mutex
	registered []string
}

func (w *fakeWatcher) RegisterStatusNotifierItem(service string) *dbus.Error {
	w.mu.Lock()
	w.registered = append(w.registered, service)
	w.mu.Unlock()
	return nil
}

func (w *fakeWatcher) services() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.registered...)
}

func startFakeWatcher(t *testing.T, addr string) *fakeWatcher {
	t.Helper()
	conn := connectTest(t, addr)
	w := &fakeWatcher{}
	if err := conn.Export(w, watcherPath, watcherIface); err != nil {
		t.Fatalf("экспорт наблюдателя: %v", err)
	}
	reply, err := conn.RequestName(watcherService, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("не удалось занять имя наблюдателя: reply=%v err=%v", reply, err)
	}
	return w
}

// С поднятым (пусть и ненастоящим) наблюдателем регистрация обязана
// пройти и сообщить наблюдателю ровно то имя шины, под которым сама
// иконка на ней зарегистрирована — "org.kde.StatusNotifierItem-<pid>-1".
func TestRegisterStatusNotifierItemSucceedsWithWatcher(t *testing.T) {
	addr := startPrivateBus(t)
	watcher := startFakeWatcher(t, addr)
	conn := connectTest(t, addr)

	ok := registerStatusNotifierItem(conn, func() {}, func() {})
	if !ok {
		t.Fatal("регистрация с поднятым наблюдателем должна пройти")
	}

	got := watcher.services()
	if len(got) != 1 || !strings.HasPrefix(got[0], "org.kde.StatusNotifierItem-") {
		t.Errorf("наблюдатель получил %v, ожидалось одно имя вида org.kde.StatusNotifierItem-<pid>-1", got)
	}
}

// Свойства должны быть доступны обычным способом D-Bus (GetAll), тем же,
// каким их будет спрашивать настоящий хост трея сразу после регистрации.
func TestStatusNotifierItemPropertiesViaGetAll(t *testing.T) {
	addr := startPrivateBus(t)
	startFakeWatcher(t, addr)
	itemConn := connectTest(t, addr)
	if !registerStatusNotifierItem(itemConn, func() {}, func() {}) {
		t.Fatal("регистрация не удалась")
	}

	client := connectTest(t, addr)
	var props map[string]dbus.Variant
	err := client.Object(itemConn.Names()[0], trayItemPath).
		Call("org.freedesktop.DBus.Properties.GetAll", 0, itemIface).
		Store(&props)
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}

	checks := map[string]string{
		"Category": "ApplicationStatus",
		"Id":       trayIconName,
		"IconName": trayIconName,
		"Title":    "Heroku",
		"Status":   "Active",
	}
	for name, want := range checks {
		v, ok := props[name]
		if !ok {
			t.Errorf("свойство %s отсутствует в ответе", name)
			continue
		}
		if got := v.Value().(string); got != want {
			t.Errorf("свойство %s = %q, ожидалось %q", name, got, want)
		}
	}
	if _, ok := props["Menu"]; !ok {
		t.Error("свойство Menu отсутствует — хост не найдёт объект меню")
	}
}

// recursionDepth=0 — только корень, без детей: хост, который явно попросил
// глубину 0, не должен получать пункты меню, которые не спрашивал.
func TestTrayMenuGetLayoutRespectsRecursionDepth(t *testing.T) {
	m := &trayMenu{}

	_, root, err := m.GetLayout(0, 0, nil)
	if err != nil {
		t.Fatalf("GetLayout(depth=0): %v", err)
	}
	if len(root.Children) != 0 {
		t.Errorf("depth=0: получено %d детей, ожидалось 0", len(root.Children))
	}

	_, root, err = m.GetLayout(0, -1, nil)
	if err != nil {
		t.Fatalf("GetLayout(depth=-1): %v", err)
	}
	if len(root.Children) != 2 {
		t.Fatalf("depth=-1: получено %d детей, ожидалось 2", len(root.Children))
	}
	first := root.Children[0].Value().(menuLayout)
	if first.ID != menuIDToggle {
		t.Errorf("первый пункт меню — id %d, ожидался %d (переключение окна)", first.ID, menuIDToggle)
	}
}

func TestTrayMenuGetGroupPropertiesFiltersByID(t *testing.T) {
	m := &trayMenu{}

	all, err := m.GetGroupProperties(nil, nil)
	if err != nil || len(all) != 2 {
		t.Fatalf("GetGroupProperties(nil) = %v, %v; ожидалось 2 пункта без фильтра", all, err)
	}

	only, err := m.GetGroupProperties([]int32{menuIDQuit}, nil)
	if err != nil || len(only) != 1 || only[0].ID != menuIDQuit {
		t.Errorf("GetGroupProperties([Quit]) = %v, %v; ожидался один пункт с id %d", only, err, menuIDQuit)
	}
}

// Клик по пункту меню приходит как Event(id, "clicked", ...) — другие
// eventID (например "hovered") и неизвестные id не должны запускать
// действие, иначе наведение мышью на пункт вело бы себя как клик по нему.
func TestTrayMenuEventDispatchesOnlyOnClick(t *testing.T) {
	var toggled, quit int
	m := &trayMenu{
		onToggle: func() { toggled++ },
		onQuit:   func() { quit++ },
	}

	m.Event(menuIDToggle, "hovered", dbus.MakeVariant(""), 0)
	if toggled != 0 {
		t.Error("hovered не должен запускать действие")
	}

	m.Event(menuIDToggle, "clicked", dbus.MakeVariant(""), 0)
	if toggled != 1 {
		t.Errorf("toggled = %d, ожидался 1 после клика по пункту переключения", toggled)
	}

	m.Event(menuIDQuit, "clicked", dbus.MakeVariant(""), 0)
	if quit != 1 {
		t.Errorf("quit = %d, ожидался 1 после клика по пункту выхода", quit)
	}

	m.Event(999, "clicked", dbus.MakeVariant(""), 0)
	if toggled != 1 || quit != 1 {
		t.Error("клик по неизвестному id не должен запускать ни одно из действий")
	}
}
