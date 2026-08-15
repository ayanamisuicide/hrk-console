package main

import (
	"os"
	"os/exec"
	"runtime"
	"time"

	wailsrt "github.com/wailsapp/wails/v2/pkg/runtime"

	"heroku-console/selfupdate"
	"heroku-console/state"
)

// guiAssetPrefix/guiAssetSuffix — имя архива с GUI-бинарником в релизе, как
// его кладёт release.yml: hrk-console-gui-$TAG-linux-amd64.tar.gz на Linux,
// hrk-console-gui-$TAG-windows-amd64.tar.gz на Windows (два разных job'а в
// release.yml, одна и та же схема имени). hkc в том же релизе носит своё
// имя (hkc-*) — selfupdate.Apply различает их по этим prefix/suffix, иначе
// скачал бы не то.
const guiAssetPrefix = "hrk-console-gui-"

func guiAssetSuffix() string {
	if runtime.GOOS == "windows" {
		return "-windows-amd64.tar.gz"
	}
	return "-linux-amd64.tar.gz"
}

// CheckForUpdate сверяется с текущим каналом (см. SetUpdateChannel). Отдельной
// проверки при старте окна идёт через CheckChannels (экран обновлений), который и так
// узнаёт оба канала. Логика запроса и сравнения версий общая с TUI на
// стабильном канале (selfupdate.Check); канал "dev" — только у GUI.
func (a *App) CheckForUpdate() UpdateCheckResult {
	a.mu.Lock()
	channel := a.ui.UpdateChannel
	a.mu.Unlock()
	r := selfupdate.CheckChannel(channel, version)
	return UpdateCheckResult{
		OK: r.OK, Available: r.Available,
		Current: r.Current, Latest: r.Latest,
		URL: r.URL, Message: r.Message, Channel: channel,
	}
}

// SetUpdateChannel переключает канал самообновления ("" — стабильный,
// "dev" — сборки с ветки dev) и сразу возвращает свежую проверку по новому
// каналу — отдельным запросом с фронтенда после переключения было бы то же
// самое действие, просто в два шага вместо одного.
func (a *App) SetUpdateChannel(channel string) UpdateCheckResult {
	a.mu.Lock()
	a.ui.UpdateChannel = channel
	ui := a.ui
	a.mu.Unlock()
	state.Save(ui)
	return a.CheckForUpdate()
}

// ChannelsResult — состояние обоих каналов разом, для экрана обновлений
// после проверок окружения. Не «что новее»: между каналами это неразрешимо
// (dev-тег вида "vX.Y.Z-dev.N" считается от своего последнего стабильного
// релиза, у двух таких тегов от разных релизов порядка нет), поэтому
// наружу идут факты — тег и когда он опубликован, — а решение остаётся за
// человеком.
type ChannelsResult struct {
	Current string                  `json:"current"`
	Channel string                  `json:"channel"` // на каком канале сидим сейчас
	Stable  selfupdate.ChannelState `json:"stable"`
	Dev     selfupdate.ChannelState `json:"dev"`
	// Offer — есть ли вообще что предлагать. Экран показывается только
	// тогда: гонять его вхолостую после каждой проверки окружения ради
	// «всё актуально» значило бы добавить лишний шаг к каждому запуску.
	Offer bool `json:"offer"`
}

// CheckChannels опрашивает stable и dev разом — их два независимых запроса
// идут параллельно внутри selfupdate.CheckBoth. Осечка одного канала не
// прячет второй: у каждого свой ok/message, и экран покажет то, что удалось
// узнать, вместо пустоты.
func (a *App) CheckChannels() ChannelsResult {
	a.mu.Lock()
	channel := a.ui.UpdateChannel
	a.mu.Unlock()

	stable, dev := selfupdate.CheckBoth(version)
	res := ChannelsResult{Current: version, Channel: channel, Stable: stable, Dev: dev}
	// Предлагать есть что, если хоть один канал ответил тегом, который не
	// равен запущенной сборке. Именно равенство, а не «новее»: сравнить
	// dev-сборку со стабильным релизом по номеру нельзя, а вот «это не то,
	// что запущено» — утверждение, верное всегда.
	res.Offer = (stable.OK && !stable.IsCurrent) || (dev.OK && !dev.IsCurrent)
	return res
}

// ApplyUpdateFrom — то же, что ApplyUpdate, но из явно названного канала, и
// с отчётом о ходе: каждый шаг selfupdate уезжает фронтенду событием
// 'update-step', по которому рисуется окно обновления. Смена канала здесь
// же и сохраняется — уехав на dev через этот экран, пользователь остаётся
// на dev и при следующих проверках, иначе окно обновилось бы до сборки, о
// которой само потом «забыло» бы.
func (a *App) ApplyUpdateFrom(channel string) ActionResult {
	a.mu.Lock()
	if a.ui.UpdateChannel != channel {
		a.ui.UpdateChannel = channel
		ui := a.ui
		a.mu.Unlock()
		state.Save(ui)
	} else {
		a.mu.Unlock()
	}

	applied, err := selfupdate.ApplyChannelProgress(channel, guiAssetPrefix, guiAssetSuffix(),
		func(p selfupdate.Progress) { a.emit("update-step", p) })
	if err != nil {
		// Отдельным событием, а не только возвратом: окно обновления живёт
		// на потоке шагов, и оборвись он молча — оно осталось бы навсегда
		// с крутящимся шагом, который уже никогда не закроется.
		a.emit("update-failed", err.Error())
		return ActionResult{OK: false, Message: err.Error()}
	}
	return ActionResult{OK: true, Message: applied}
}

// ApplyUpdate скачивает GUI-бинарник из последнего релиза выбранного канала
// и подменяет им себя на диске (selfupdate.ApplyChannel — общая логика с
// TUI на стабильном канале, dev — только здесь). Не вызывается сам по себе
// в фоне — только по явному клику из фронтенда (см. update-status): подмена
// собственного исполняемого файла необратима без переустановки, и
// молчаливый автозапуск для такого не подходит, в отличие от простой
// проверки обновлений. Перезапуск (RestartApp) — отдельный шаг.
func (a *App) ApplyUpdate() ActionResult {
	a.mu.Lock()
	channel := a.ui.UpdateChannel
	a.mu.Unlock()
	return a.ApplyUpdateFrom(channel)
}

// healPendingUpdate подхватывает обновление, которое ApplyUpdate когда-то
// скачал (см. RestartApp), но подмена так и не завершилась, — например,
// предыдущий явный клик «перезапустить» запустил своп-скрипт, а тот не
// уложился в отведённые попытки (антивирус подержал свежескачанный exe
// дольше обычного). Без этой проверки «.new» так и лежал бы рядом с
// прежним exe навсегда, а пользователю приходилось бы вручную стирать
// старый файл и переименовывать новый. Тот же своп-скрипт просто
// запускается заново при каждом обычном старте окна, без единого клика —
// startup() зовёт это самым первым делом, до всего остального: если
// обновление ждёт применения, окну всё равно предстоит тут же закрыться.
func (a *App) healPendingUpdate() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	staged := selfupdate.PendingUpdate(exe)
	if staged == "" {
		return false
	}
	if err := restartWithSwap(exe, staged); err != nil {
		return false
	}
	a.quitSoon()
	return true
}

// quitSoon — небольшая задержка перед Quit, чтобы фронтенд успел получить
// ответ на вызвавший её запрос и показать что-то осмысленное, прежде чем
// окно исчезнет.
func (a *App) quitSoon() {
	go func() {
		time.Sleep(300 * time.Millisecond)
		wailsrt.Quit(a.ctx)
	}()
}

// RestartApp поднимает новый процесс того же бинарника (уже подменённого
// ApplyUpdate) и завершает текущий. Отдельный явный шаг, а не часть
// ApplyUpdate, — обновление на диске и разрыв текущей сессии окна должны
// быть двумя разными решениями пользователя, не одним неожиданным.
func (a *App) RestartApp() ActionResult {
	exe, err := os.Executable()
	if err != nil {
		return ActionResult{OK: false, Message: err.Error()}
	}

	// На Windows ApplyUpdate не смог подменить себя сразу (файл заблокирован,
	// пока этот процесс из него выполняется) и оставил новую версию рядом —
	// restartWithSwap ждёт, пока этот процесс действительно завершится,
	// прежде чем подменять файл. На Linux staged всегда пусто: там подмена
	// уже произошла в момент ApplyUpdate, и RestartApp — просто обычный
	// перезапуск.
	if staged := selfupdate.PendingUpdate(exe); staged != "" {
		if err := restartWithSwap(exe, staged); err != nil {
			return ActionResult{OK: false, Message: err.Error()}
		}
	} else {
		// Тот же приём "породить копию себя и завершиться", что и у TUI на
		// Windows (internal/tui/restart_other.go:RestartSelf) — здесь без
		// его недостатков для терминала, GUI никакой терминал не держит.
		cmd := exec.Command(exe)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return ActionResult{OK: false, Message: err.Error()}
		}
	}

	a.quitSoon()
	return ActionResult{OK: true}
}
