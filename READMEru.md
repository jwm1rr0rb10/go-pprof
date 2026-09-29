# go-pprof

[English version](README.md)

Небольшая обёртка над [`net/http/pprof`](https://pkg.go.dev/net/http/pprof) для Go-приложений: подключить pprof к своему mux или запустить отдельный pprof-сервер на localhost с graceful shutdown, а при необходимости закрыть эндпоинты basic auth или списком разрешённых IP.

Без зависимостей, кроме стандартной библиотеки. Требуется Go 1.21+.

## Установка

```
go get github.com/jwm1rr0rb10/go-pprof
```

## Использование

### Отдельный сервер на localhost (рекомендуется)

pprof на отдельном порту, привязанном к `127.0.0.1`, не пересекается с публичным API.

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

	// Config{} слушает 127.0.0.1:6060.
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

`Run` открывает сокет синхронно, поэтому ошибки вроде «address already in use» возвращаются сразу. После штатной остановки `Run` возвращает `nil`, так что отфильтровывать `context.Canceled` не нужно.

Чтобы слушать случайный свободный порт или Unix-сокет, создайте listener сами и передайте его в `Serve`:

```go
ln, err := net.Listen("tcp", "127.0.0.1:0")
if err != nil {
	log.Fatal(err)
}
log.Printf("pprof на http://%s/debug/pprof/", ln.Addr())
go srv.Serve(ctx, ln)
```

### Подключение к существующему mux

```go
mux := http.NewServeMux()
pprof.Register(mux)
mux.HandleFunc("/api/health", healthHandler)

// Только в доверенной сети: pprof теперь доступен везде, где доступен mux.
http.ListenAndServe("127.0.0.1:8080", mux)
```

Если этот mux доступен снаружи, защитите эндпоинты (см. ниже). `pprof.Handler(...)` возвращает готовый `http.Handler`, если удобнее смонтировать его самостоятельно.

### Защита эндпоинтов

`Register` и `Config.Middlewares` принимают middleware. Первый в списке оборачивает все остальные.

```go
allow, err := pprof.AllowNetworks("10.0.0.0/8", "127.0.0.1")
if err != nil {
	log.Fatal(err)
}

pprof.Register(mux, allow, pprof.BasicAuth("admin", os.Getenv("PPROF_PASSWORD")))

// или для отдельного сервера:
srv := pprof.NewServer(pprof.Config{
	Host:        "0.0.0.0",
	Middlewares: []pprof.Middleware{allow},
})
```

`BasicAuth` сравнивает логин и пароль за константное время и паникует при пустом логине или пароле. Basic auth передаёт данные открытым текстом, поэтому используйте его поверх TLS или в приватной сети.

`AllowNetworks` принимает CIDR-префиксы и отдельные IP, IPv4 и IPv6. Проверяется только `r.RemoteAddr`, а `X-Forwarded-For` игнорируется, потому что клиент может его подделать. За reverse proxy будет виден адрес прокси.

Middleware может быть любая функция `func(http.Handler) http.Handler`, так что можно подключить собственную авторизацию.

## Конфигурация

```go
pprof.Config{
	Host:              "127.0.0.1",      // по умолчанию
	Port:              6060,             // по умолчанию
	ReadHeaderTimeout: 10 * time.Second, // по умолчанию, защита от медленных клиентов
	ShutdownTimeout:   15 * time.Second, // по умолчанию, лимит graceful shutdown
	Middlewares:       nil,
}
```

Нулевые значения заменяются значениями по умолчанию. `NewConfig(host, port, readHeaderTimeout)` оставлен для обратной совместимости. `WriteTimeout` намеренно не задаётся: `/profile` и `/trace` отдают данные столько, сколько запросил клиент (для `/profile` по умолчанию 30 секунд).

`Server` одноразовый: после остановки его нельзя запустить снова. Его методы можно вызывать из разных горутин.

## Безопасность

Никогда не открывайте pprof в интернет. Профили раскрывают содержимое памяти, аргументы командной строки, стеки горутин и внутреннее устройство приложения, а через `/profile` и `/trace` можно нагрузить CPU.

Пакет импортирует `net/http/pprof`, а его функция `init` регистрирует все хендлеры в `http.DefaultServeMux`. Никакая обёртка не может этому помешать. Если приложение обслуживает `http.DefaultServeMux` (например, `http.ListenAndServe(addr, nil)` или `http.Handle(...)`), pprof будет доступен и там. Для публичных серверов всегда используйте собственный `http.ServeMux`.

## Эндпоинты

| Эндпоинт | Описание |
| --- | --- |
| `/debug/pprof/` | Главная страница |
| `/debug/pprof/profile` | CPU-профиль (`?seconds=N`, по умолчанию 30) |
| `/debug/pprof/heap` | Выборка памяти в куче |
| `/debug/pprof/allocs` | Прошлые аллокации памяти |
| `/debug/pprof/goroutine` | Стеки всех горутин |
| `/debug/pprof/block` | Блокировки на примитивах синхронизации* |
| `/debug/pprof/mutex` | Конкуренция за мьютексы* |
| `/debug/pprof/threadcreate` | Стеки, создавшие потоки ОС |
| `/debug/pprof/trace` | Трассировка выполнения (`?seconds=N`, по умолчанию 1) |
| `/debug/pprof/cmdline` | Командная строка программы |
| `/debug/pprof/symbol` | Поиск символов по адресам |

\* Профили block и mutex пустые, пока не включены через `runtime.SetBlockProfileRate` и `runtime.SetMutexProfileFraction`.

Пример:

```
go tool pprof -http=:8081 http://127.0.0.1:6060/debug/pprof/profile?seconds=10
```

## Разработка

```
make race   # тесты с race-детектором
make cover  # отчёт о покрытии
make lint   # staticcheck
```

## Лицензия

[MIT](LICENSE) © Raman Zaitsau
