package main

import "github.com/godbus/dbus/v5"

// Реализация com.canonical.dbusmenu — минимальная, но настоящая: плоское
// меню из двух пунктов (без вложенных подменю, без чекбоксов и разделителей,
// они здесь просто не нужны). Полноценной интроспекции (Introspectable) для
// этого интерфейса и для org.kde.StatusNotifierItem нет — хосты трея
// (appindicator-расширение, KStatusNotifierItem, лотки Cinnamon/MATE/Xfce)
// реализуют фиксированный протокол по спецификации и не спрашивают
// интроспекцию, чтобы понять, как вызывать Activate или GetLayout: это
// не срезанный угол ради экономии времени, а то, что реально нужно этим
// клиентам.
const (
	menuIDToggle = int32(1)
	menuIDQuit   = int32(2)
)

// menuLayout — DBus-структура STRUCT(INT32, DICT<STRING,VARIANT>, ARRAY of VARIANT),
// та же что описывает сама спецификация dbusmenu: узел, его свойства и дети,
// каждый ребёнок — вариант, оборачивающий такую же структуру рекурсивно.
// Меню плоское, поэтому у детей Children всегда пуст, но тип всё равно
// рекурсивный — этого требует сигнатура метода.
type menuLayout struct {
	ID         int32
	Properties map[string]dbus.Variant
	Children   []dbus.Variant
}

type menuItemProps struct {
	ID         int32
	Properties map[string]dbus.Variant
}

func trayMenuItems() []menuLayout {
	return []menuLayout{
		{ID: menuIDToggle, Properties: map[string]dbus.Variant{
			"label": dbus.MakeVariant("Показать / скрыть"),
			"type":  dbus.MakeVariant("standard"),
		}, Children: []dbus.Variant{}},
		{ID: menuIDQuit, Properties: map[string]dbus.Variant{
			"label": dbus.MakeVariant("Выход"),
			"type":  dbus.MakeVariant("standard"),
		}, Children: []dbus.Variant{}},
	}
}

type trayMenu struct {
	onToggle func()
	onQuit   func()
}

// GetLayout — recursionDepth 0 по спецификации значит "только корень, без
// детей" (хост потом дозапросит нужную глубину сам); -1 — "разворачивай
// всё". У плоского меню разница только в том, приезжают ли те же два
// пункта сразу или отдельным запросом, поведение в обоих случаях верное.
func (m *trayMenu) GetLayout(parentID, recursionDepth int32, propertyNames []string) (uint32, menuLayout, *dbus.Error) {
	root := menuLayout{
		ID:         0,
		Properties: map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")},
		Children:   []dbus.Variant{},
	}
	if recursionDepth == 0 {
		return 1, root, nil
	}
	for _, item := range trayMenuItems() {
		root.Children = append(root.Children, dbus.MakeVariant(item))
	}
	return 1, root, nil
}

func (m *trayMenu) GetGroupProperties(ids []int32, propertyNames []string) ([]menuItemProps, *dbus.Error) {
	want := make(map[int32]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var out []menuItemProps
	for _, item := range trayMenuItems() {
		if len(ids) > 0 && !want[item.ID] {
			continue
		}
		out = append(out, menuItemProps{ID: item.ID, Properties: item.Properties})
	}
	return out, nil
}

// AboutToShow — по спецификации может лениво пересобрать меню перед
// показом. Наше меню статично, обновлять нечего, поэтому false ("макет не
// поменялся") всегда корректный ответ.
func (m *trayMenu) AboutToShow(id int32) (bool, *dbus.Error) { return false, nil }

// Event — хост шлёт eventID "clicked" по клику на пункт; "hovered" и прочие
// нас не интересуют, тем же best-effort приёмом, что и у остального
// трея, — неизвестное событие просто игнорируется, а не считается ошибкой.
func (m *trayMenu) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID != "clicked" {
		return nil
	}
	switch id {
	case menuIDToggle:
		m.onToggle()
	case menuIDQuit:
		m.onQuit()
	}
	return nil
}

func exportTrayMenu(conn *dbus.Conn, onToggle, onQuit func()) bool {
	menu := &trayMenu{onToggle: onToggle, onQuit: onQuit}
	return conn.Export(menu, trayMenuPath, "com.canonical.dbusmenu") == nil
}
