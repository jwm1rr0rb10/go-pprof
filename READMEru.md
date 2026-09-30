# go-pprof

[English version](README.md)

Безопасный для продакшена [`net/http/pprof`](https://pkg.go.dev/net/http/pprof) для Go-сервисов: отдельный pprof-сервер на localhost с graceful shutdown, лимиты, которые защищают нагруженный сервис от дорогих запросов к профилировщику, и контроль доступа через basic auth или список разрешённых IP.

Без зависимостей, кроме стандартной библиотеки. Требуется Go 1.21+.

## Почему не просто импортировать net/http/pprof

Стандартные хендлеры делают ровно то, что попросил клиент. На нагруженном сервисе некоторые запросы обходятся дорого:

| Запрос | Что происходит |
| --- | --- |
| `/profile?seconds=3600` | Накладные расходы CPU-профилирования в течение часа; второй профиль падает с 500 |
| `/trace?seconds=600` | Накладные расходы трассировки и сотни мегабайт ответа |
| `/heap?seconds=3600` | Соединение занято на час (дельта-профиль) |
| `/heap?gc=1` в цикле | Полная сборка мусора на каждый вызов |
| `/goroutine?debug=2` | Остановка программы для дампа всех горутин; огромный ответ при 100k+ горутин |
| `/goroutine` или `/heap` в цикле | Постоянная нагрузка от профилирования; при 500k горутин каждый вызов обходит все их стеки |
| Медленный или зависший клиент | Держит соединение бесконечно (для потоковых эндпоинтов общий write timeout невозможен) |
| `POST /symbol` с медленным или огромным телом | Стандартный хендлер читает тело до EOF и держит соединение |

Пакет ставит перед хендлерами защитный слой с безопасными значениями по умолчанию, чтобы неаккуратный скрипт или любопытный коллега не замедлили прод.

## Установка

```
go get github.com/jwm1rr0rb10/go-pprof
```

## Использование

### Отдельный сервер (рекомендуется)

```go
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/jwm1rr0rb10/go-pprof"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 127.0.0.1:6060 с лимитами по умолчанию.
	srv := pprof.NewServer(pprof.Config{})

	go func() {
		if err := srv.Run(ctx); err != nil {
			log.Printf("pprof: %v", err)
		}
	}()

	// ... код приложения ...

	<-ctx.Done()
}
```

`Run` открывает сокет синхронно, поэтому ошибки вроде «address already in use» возвращаются сразу. После штатной остановки `Run` возвращает `nil`.

Чтобы слушать случайный свободный порт или Unix-сокет, передайте свой listener в `Serve`:

```go
ln, err := net.Listen("tcp", "127.0.0.1:0")
if err != nil {
	log.Fatal(err)
}
log.Printf("pprof на http://%s/debug/pprof/", ln.Addr())
go srv.Serve(ctx, ln)
```

### Конфигурация для продакшена

```go
srv := pprof.NewServer(pprof.Config{
	Host:        "0.0.0.0", // доступен только из внутренней сети
	Middlewares: []pprof.Middleware{allow, pprof.BasicAuth("admin", os.Getenv("PPROF_PASSWORD"))},
	Limits: pprof.Limits{
		MaxProfileDuration: 30 * time.Second,
		MaxTraceDuration:   5 * time.Second,
		MaxConcurrent:      2,
		DisabledEndpoints:  []string{"cmdline"}, // во флагах могут быть секреты
		OnRequest: func(i pprof.RequestInfo) {
			slog.Info("pprof request",
				"endpoint", i.Endpoint, "remote", i.RemoteAddr, "status", i.Status,
				"seconds", i.Seconds, "duration", i.Duration, "rejected", i.Rejected)
		},
	},
	// Включить /block и /mutex с низкими накладными расходами.
	BlockProfileRate:     pprof.RecommendedBlockProfileRate,
	MutexProfileFraction: pprof.RecommendedMutexProfileFraction,
})
```

Middleware выполняются до лимитов, поэтому неавторизованные запросы никогда не занимают слот параллельности.

### Подключение к существующему mux

```go
mux := http.NewServeMux()
pprof.Register(mux, pprof.Guard(pprof.Limits{}))
mux.HandleFunc("/api/health", healthHandler)

// Только в доверенной сети: pprof теперь доступен везде, где доступен mux.
http.ListenAndServe("127.0.0.1:8080", mux)
```

`Register` сам лимиты не добавляет: передайте `Guard` последним middleware, после авторизации. `pprof.Handler(...)` возвращает готовый `http.Handler`, если удобнее смонтировать его самостоятельно.

## Лимиты

Нулевое значение `pprof.Limits` — безопасная конфигурация для продакшена.

| Поле | По умолчанию | Что делает |
| --- | --- | --- |
| `MaxProfileDuration` | 60s | Ограничивает `seconds` у `/profile` и дельта-профилей (`/heap?seconds=N`, ...). Более длинные запросы урезаются, а не отклоняются. |
| `MaxTraceDuration` | 10s | Ограничивает `seconds` у `/trace`. |
| `MaxConcurrent` | 4 | Сколько pprof-запросов обслуживается одновременно; лишние получают `429` с `Retry-After`. Независимо от этого одновременно выполняется не больше одного `/profile` и одного `/trace`; второй получает `429`, и `Retry-After` равен времени, которое ещё нужно текущему. |
| `MinInterval` | 1s | Минимальный интервал между запросами к одному и тому же профилю (`/heap`, `/goroutine`, `/profile`, ...). У каждого эндпоинта свои часы; `/`, `/cmdline` и `/symbol` не ограничиваются. Слишком частые запросы получают `429` с точным `Retry-After`. |
| `WriteTimeout` | 30s | Время на запись ответа после сбора данных. Дедлайн записи = время сбора + `WriteTimeout`, так что зависшие клиенты освобождают слот. |
| `ReadTimeout` | 10s | Время на отправку тела `POST /symbol`; более медленные клиенты получают `408`. |
| `MaxSymbolBodyBytes` | 1 MiB | Максимальный размер тела `POST /symbol`; больше — `413`. |
| `AllowForcedGC` | false | `/heap?gc=1` отклоняется с `403`, если не включено. |
| `AllowFullGoroutineDump` | false | `/goroutine?debug=2` отклоняется с `403`, если не включено. `/goroutine?debug=1` (агрегированные стеки) работает всегда. |
| `DisabledEndpoints` | нет | Эндпоинты, которые возвращают `404`: `"index"`, `"cmdline"`, `"profile"`, `"symbol"`, `"trace"` или имя профиля, например `"heap"`. |
| `OnRequest` | nil | Вызывается после каждого pprof-запроса, включая отклонённые, с `RequestInfo`. Для аудита и метрик; должен работать быстро. |

Отрицательная длительность, `MaxConcurrent` или `MaxSymbolBodyBytes` означают «без ограничения». `POST` принимает только `/symbol`; остальные эндпоинты отвечают `405`, потому что стандартные хендлеры читают параметры и из тела формы, и через него можно было бы обойти ограничение длительности.

## Контроль доступа

```go
allow, err := pprof.AllowNetworks("10.0.0.0/8", "127.0.0.1")
if err != nil {
	log.Fatal(err)
}
```

`AllowNetworks` принимает CIDR-префиксы и отдельные IP, IPv4 и IPv6. Проверяется только `r.RemoteAddr`, а `X-Forwarded-For` игнорируется, потому что клиент может его подделать; за reverse proxy будет виден адрес прокси.

`BasicAuth` сравнивает логин и пароль за константное время и паникует при пустом логине или пароле. Basic auth передаёт данные открытым текстом, поэтому используйте его поверх TLS или в приватной сети.

Middleware может быть любая функция `func(http.Handler) http.Handler`.

## Настройки сервера

```go
pprof.Config{
	Host:                 "127.0.0.1",      // по умолчанию
	Port:                 6060,             // по умолчанию
	ReadHeaderTimeout:    10 * time.Second, // по умолчанию
	IdleTimeout:          60 * time.Second, // по умолчанию
	ShutdownTimeout:      15 * time.Second, // по умолчанию
	Middlewares:          nil,
	Limits:               pprof.Limits{},   // безопасные значения
	BlockProfileRate:     0,                // 0 = не менять настройку рантайма
	MutexProfileFraction: 0,                // 0 = не менять настройку рантайма
	ResetProfileRates:    false,            // true = откатить две настройки выше при остановке
}
```

Нулевые значения заменяются значениями по умолчанию. Общего `WriteTimeout` у сервера нет, потому что `/profile` и `/trace` отдают данные столько, сколько запрошено; вместо него `Guard` ставит дедлайн записи для каждого запроса. `BlockProfileRate` и `MutexProfileFraction` — глобальные настройки рантайма, они применяются при старте сервера; с `ResetProfileRates` они откатываются, когда `Run`/`Serve` возвращает управление (block rate ставится в 0, mutex fraction восстанавливается). Рекомендуемые значения (`10_000` нс и `100`) дают низкие накладные расходы; `1` записывает каждое событие и слишком дорого для продакшена.

`Server` одноразовый: после остановки его нельзя запустить снова. Его методы можно вызывать из разных горутин.

## Безопасность

Никогда не открывайте pprof в интернет. Профили раскрывают содержимое памяти, аргументы командной строки, стеки горутин и внутреннее устройство приложения.

Пакет импортирует `net/http/pprof`, а его функция `init` регистрирует все хендлеры в `http.DefaultServeMux`. Никакая обёртка не может этому помешать. Если приложение обслуживает `http.DefaultServeMux` (например, `http.ListenAndServe(addr, nil)` или `http.Handle(...)`), pprof будет доступен и там, причём без лимитов. Для публичных серверов всегда используйте собственный `http.ServeMux`.

## Непрерывное профилирование

В больших продакшен-системах HTTP-pprof лучше всего работает вместе с непрерывным профилировщиком (Pyroscope, Grafana Profiles, Parca, Datadog). Он постоянно снимает профили с низкими накладными расходами и хранит историю, так что можно посмотреть, что происходило во время ночного инцидента. Защищённый HTTP-pprof остаётся инструментом для точечных снимков.

## Эндпоинты

| Эндпоинт | Описание |
| --- | --- |
| `/debug/pprof/` | Главная страница |
| `/debug/pprof/profile` | CPU-профиль (`?seconds=N`, по умолчанию 30, ограничен `MaxProfileDuration`) |
| `/debug/pprof/heap` | Выборка памяти в куче |
| `/debug/pprof/allocs` | Прошлые аллокации памяти |
| `/debug/pprof/goroutine` | Стеки всех горутин |
| `/debug/pprof/block` | Блокировки на примитивах синхронизации* |
| `/debug/pprof/mutex` | Конкуренция за мьютексы* |
| `/debug/pprof/threadcreate` | Стеки, создавшие потоки ОС |
| `/debug/pprof/trace` | Трассировка выполнения (`?seconds=N`, по умолчанию 1, ограничена `MaxTraceDuration`) |
| `/debug/pprof/cmdline` | Командная строка программы |
| `/debug/pprof/symbol` | Поиск символов по адресам |

\* Пустые, пока не включены через `BlockProfileRate` / `MutexProfileFraction` (или `runtime.SetBlockProfileRate` / `runtime.SetMutexProfileFraction`).

Пример:

```
go tool pprof -http=:8081 http://127.0.0.1:6060/debug/pprof/profile?seconds=10
```

## Разработка

```
make race   # тесты с race-детектором
make cover  # отчёт о покрытии
make lint   # staticcheck
make bench  # бенчмарки
```

## Лицензия

[MIT](LICENSE) © Raman Zaitsau
