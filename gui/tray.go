package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// Системный трей — напрямую по D-Bus, протокол StatusNotifierItem
// (freedesktop/KDE), без cgo и без GTK-библиотек трея. В 1.8.0 трей
// сознательно не делали именно из-за них: обычные Go-обёртки (systray и
// подобные) тянут cgo+libappindicator, а GNOME без отдельного расширения
// такой индикатор просто не показывает — ставить это вслепую, без
// возможности проверить на месте, не имело смысла (см. CHANGELOG 1.8.0).
//
// StatusNotifierItem — сам протокол, который слушают и appindicator-
// расширение GNOME, и нативные лотки Cinnamon/MATE/Xfce (основные
// окружения Mint, см. termwin) — а не обёртка над одним из них,
// поэтому cgo не нужен вовсе: только чистый D-Bus (godbus/dbus, уже
// присутствует в графе зависимостей Wails как транзитивная). Остаётся тот
// же честный риск, что и раньше, — по ту сторону D-Bus всегда десктопное
// окружение, которое нельзя проверить из этого окружения разработки — но
// сам протокол и код регистрации проверены тестами против настоящего
// dbus-daemon (tray_test.go), в отличие от прежней попытки вслепую.
const (
	trayItemPath   = dbus.ObjectPath("/StatusNotifierItem")
	trayMenuPath   = dbus.ObjectPath("/MenuBar")
	watcherService = "org.kde.StatusNotifierWatcher"
	watcherPath    = dbus.ObjectPath("/StatusNotifierWatcher")
	watcherIface   = "org.kde.StatusNotifierWatcher"
	itemIface      = "org.kde.StatusNotifierItem"
)

// trayIconName — то же имя, под которым install.sh кладёт иконку в тему
// значков (~/.local/share/icons/hicolor/512x512/apps/hrk-console-gui.png,
// APP_ID в install.sh). IconName у StatusNotifierItem ищется по теме
// значков ровно так же, как ярлык в меню приложений — второй раз
// устанавливать иконку никуда не нужно.
const trayIconName = "hrk-console-gui"

// Tray — трей-иконка. Best-effort: если сессионной D-Bus шины нет или
// наблюдателя (StatusNotifierWatcher) никто не поднял — десктоп без трея
// вовсе, или ещё не готов — Start просто не показывает иконку и не
// сообщает об этом ни ошибкой, ни в лог, тем же приёмом, что и
// notifyDesktop: отсутствие трея не повод падать или шуметь.
type Tray struct {
	mu   sync.Mutex
	conn *dbus.Conn
}

// Start подключается к сессионной шине, регистрирует объект
// StatusNotifierItem и его меню, и просит наблюдателя показать иконку.
// Вызывается в фоне (см. App.startup) — сама попытка не должна задерживать
// открытие окна. Один-единственный заход, без повторов: приложение
// запускает пользователь вручную, к этому моменту трей (если он вообще
// есть в системе) уже поднят, ждать его появления после старта окна не
// нужно — то же решение, что и у проверки обновлений (разовая попытка, не
// вечный опрос).
func (t *Tray) Start(onToggle, onQuit func()) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return
	}
	if !registerStatusNotifierItem(conn, onToggle, onQuit) {
		conn.Close()
		return
	}
	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
}

// Close закрывает соединение, если трей был поднят. Вызывающий (App.shutdown)
// не обязан знать, поднялся ли трей вообще — метод безопасен и без него.
func (t *Tray) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conn != nil {
		t.conn.Close()
		t.conn = nil
	}
}

type sniItem struct {
	onToggle func()
}

// Activate — по спецификации, левый клик по иконке. Единственное
// действие трея помимо меню — свернуть/развернуть окно, поэтому
// SecondaryActivate (обычно средний клик) ведёт себя так же: разводить
// два разных жеста на одно и то же действие незачем.
func (s *sniItem) Activate(x, y int32) *dbus.Error {
	s.onToggle()
	return nil
}
func (s *sniItem) SecondaryActivate(x, y int32) *dbus.Error {
	s.onToggle()
	return nil
}

// ContextMenu — по спецификации вызывается, если у хоста не получилось
// самому показать объект Menu. Ничего сверх этого не делаем: обработка
// правого клика — дело меню (traymenu.go), а не этого метода.
func (s *sniItem) ContextMenu(x, y int32) *dbus.Error                 { return nil }
func (s *sniItem) Scroll(delta int32, orientation string) *dbus.Error { return nil }

// registerStatusNotifierItem — вся логика подготовки и регистрации,
// отдельно от Tray.Start ради тестируемости: тест может дать сюда conn
// от настоящего dbus-daemon и проверить результат, не поднимая ни App,
// ни окно.
func registerStatusNotifierItem(conn *dbus.Conn, onToggle, onQuit func()) bool {
	item := &sniItem{onToggle: onToggle}
	if err := conn.Export(item, trayItemPath, itemIface); err != nil {
		return false
	}

	_, err := prop.Export(conn, trayItemPath, prop.Map{
		itemIface: {
			"Category":   {Value: "ApplicationStatus", Emit: prop.EmitFalse},
			"Id":         {Value: trayIconName, Emit: prop.EmitFalse},
			"Title":      {Value: "Heroku", Emit: prop.EmitFalse},
			"Status":     {Value: "Active", Emit: prop.EmitFalse},
			"IconName":   {Value: trayIconName, Emit: prop.EmitFalse},
			"ItemIsMenu": {Value: false, Emit: prop.EmitFalse},
			"Menu":       {Value: trayMenuPath, Emit: prop.EmitFalse},
		},
	})
	if err != nil {
		return false
	}

	if !exportTrayMenu(conn, onToggle, onQuit) {
		return false
	}

	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil || (reply != dbus.RequestNameReplyPrimaryOwner && reply != dbus.RequestNameReplyAlreadyOwner) {
		return false
	}

	// Осечка здесь и значит "трея в этой системе нет" — не сеть, не диск,
	// а буквально "с той стороны никто не отвечает", поэтому единственная
	// осмысленная реакция — молча отступить, как и договорились в
	// комментарии к Tray.
	call := conn.Object(watcherService, watcherPath).Call(watcherIface+".RegisterStatusNotifierItem", 0, name)
	return call.Err == nil
}
