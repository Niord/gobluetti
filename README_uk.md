# gobluetti

[🇬🇧 English](./README.md) | [🇺🇦 Українська](./README_uk.md)

Бібліотека на Go для моніторингу та керування зарядними станціями BLUETTI через хмару BLUETTI.

Це порт на Go хмарної частини офіційної
[інтеграції BLUETTI для Home Assistant](https://github.com/bluetti-official/bluetti-home-assistant),
без жодних залежностей від Home Assistant. Проєкт неофіційний і не пов'язаний з BLUETTI.

> **Статус: рання розробка.** Працюють вхід, список станцій та їхній стан, команди й сповіщення
> про зміни в реальному часі; API ще може змінитися. Не підключайте цей модуль до першого релізу
> з тегом.

## Межі проєкту

- **Тільки хмара.** Бібліотека працює з хмарою BLUETTI через ваш акаунт BLUETTI (OAuth2).
  Потрібен доступ до інтернету, а станція має бути онлайн у застосунку BLUETTI.
- **Без Bluetooth.** Локальне керування через BLE не входить у проєкт (див. нижче).
- Підтримуються станції, позначені «Cloud Control» у
  [таблиці підтримки upstream](https://github.com/bluetti-official/bluetti-home-assistant#-power-station-support-list),
  наприклад AC300 та Apex 300 (AP300).
- Заплановані можливості повторюють upstream: список пристроїв, стан (заряд батареї, потужність
  PV, мережі, виходів AC і DC), перемикачі виходів AC/DC, режими ECO, режим роботи та оновлення
  в реальному часі.

## Чому без Bluetooth

Офіційна інтеграція не містить реалізації протоколу BLE у своєму коді. Шифрування і розбір
кадрів знаходяться в закритій нативній бібліотеці, яку вона завантажує під час роботи. Ця
бібліотека зібрана лише під Linux і CPython, і Go не може її використати. Community-реалізації
спираються на ключі, витягнуті із застосунку BLUETTI. Ми не хочемо поширювати ці ключі чи
прив'язувати бібліотеку до закритого бінарника, тому gobluetti працює лише через хмару.

Якщо вам потрібне локальне керування через Bluetooth, подивіться
[bluetti-bt-lib](https://github.com/Patrick762/bluetti-bt-lib) (Python).

## Вимоги

- Go 1.26 або новіший.
- Акаунт BLUETTI зі станцією, доданою в застосунку BLUETTI.
- Одна залежність: `golang.org/x/net` від команди Go, лише для websocket.

## Встановлення

```bash
go get github.com/Niord/gobluetti
```

## Вхід в акаунт

Кожен виклик потребує OAuth2-токена вашого акаунта BLUETTI. Отримати його можна трьома способами.

**Командний рядок, для headless-сервісів.** Виконайте вхід на машині з браузером і скопіюйте
файл токена на сервер:

```bash
go run github.com/Niord/gobluetti/cmd/gobluetti-login@latest -o token.json
# пізніше оновіть збережений токен без браузера:
go run github.com/Niord/gobluetti/cmd/gobluetti-login@latest -refresh token.json
```

**З Go, на машині з браузером.** `LoginLocal` слухає `127.0.0.1`, чекає, доки браузер
повернеться зі сторінки входу BLUETTI, і повертає токен:

```go
var auth gobluetti.Auth // нульове значення: BLUETTI SSO з ідентичністю клієнта за замовчуванням

tok, err := auth.LoginLocal(ctx, func(url string) error {
	fmt.Println("Open this URL to log in:", url)
	return nil
})
// Збережіть tok (він серіалізується в JSON) і оновлюйте його до закінчення терміну дії:
tok, err = auth.Refresh(ctx, tok)
```

**З вебзастосунку.** Використовуйте власний redirect URL з `AuthCodeURL` та `Exchange`; див.
`ExampleAuth_Exchange` у документації пакета.

`Refresh` повертає `*OAuthError` з `Code == "invalid_grant"`, коли refresh token більше не
дійсний; у такому разі увійдіть знову.

Токен дає повний доступ до акаунта. `gobluetti-login` створює файли токена з правами `0600`;
не додавайте їх у систему контролю версій і в бекапи, які можуть читати інші.

## Робота з клієнтом

```go
c := gobluetti.NewClient(nil, tok)
c.OnToken = func(tok *gobluetti.Token) { /* збережіть його */ }

devs, err := c.Devices(ctx) // усі станції з їхніми станами
soc, _ := devs[0].State(gobluetti.FnSOC)
fmt.Println(devs[0].Model, soc.Value, soc.Unit)

err = c.Set(ctx, devs[0].SN, gobluetti.FnACOutput, "0") // вимкнути вихід AC

err = c.Watch(ctx, func(sn string) { // блокує; викликається, коли станція змінилася
	dev, _ := c.Device(ctx, sn)
	// ...
})
```

- Станція — це список станів, точно такий, як його описує хмара. Кожен стан має код функції,
  значення-рядок і тип: `SENSOR`, `SWITCH` (`"1"`/`"0"`) або `SELECT` з варіантами в `Options`.
  Константи `Fn*` називають коди, відомі офіційній інтеграції. Запустіть `gobluetti device <sn>`,
  щоб побачити, що повідомляє ваша станція.
- Клієнт сам оновлює токен і викликає `OnToken` з кожним новим. **Зберігайте його**: BLUETTI
  змінює refresh token при кожному оновленні, тож старий токен перестає працювати. Коли оновити
  токен неможливо, виклики повертають `ErrLoginRequired`: увійдіть знову.
- **Один раз прив'яжіть кожну станцію** через `c.Bind(ctx, sn)` (або `gobluetti bind <sn>`), як
  це робить офіційна інтеграція під час налаштування. Доти команди повертають `msgCode 403`
  («пристрій не належить поточному користувачу»), хоча станція є в списку.
- `Client` безпечний для паралельного використання.

## Командний рядок

```bash
go install github.com/Niord/gobluetti/cmd/gobluetti@latest
gobluetti devices                    # серійний номер, модель, назва, online/offline
gobluetti device <sn>                # усі стани: код, тип, значення, варіанти
gobluetti set <sn> SetACECO 1        # одна команда; перемикає реальну станцію
gobluetti watch                      # друкує рядок щоразу, коли станція змінюється
gobluetti bind <sn> [<sn>]           # один раз для кожної станції, перед першим set
```

Утиліта читає `~/.config/gobluetti/token.json` (змінити можна через `-token`), тобто файл, який
пише `gobluetti-login -o`, і зберігає туди оновлені токени.

## Безпека

Бібліотека може перемикати виходи реальної зарядної станції. Вимкнення виходу AC або DC
знеструмлює все, що до нього підключено.

## Ліцензія

[MIT](./LICENSE). Містить код, портований з інтеграції BLUETTI для Home Assistant, Copyright (c) 2025 Bluetti.
