# pprof

Простая, безопасная и production-ready библиотека для запуска **pprof** профилирования в Go-приложениях.

## Возможности

- Полная поддержка **всех** стандартных pprof-эндпоинтов (`/debug/pprof/`)
- Удобная регистрация обработчиков в любой существующий `http.ServeMux`
- Graceful shutdown с поддержкой `context.Context`
- Безопасные настройки по умолчанию (`127.0.0.1:6060`)
- Чистый, хорошо протестированный и документированный код
- Минималистичный и удобный API

## Установка

Просто добавьте пакет в свой проект:

```bash
github.com/jwm1rr0rb10/go-pprof
```

---

## Использование
1. Регистрация в существующий HTTP-сервер (рекомендуемый способ)


```go
package main

import (
    "net/http"

    "github.com/jwm1rr0rb10/go-pprof"
)

func main() {
    mux := http.NewServeMux()

    // Добавляем все pprof-эндпоинты одной строкой
    pprof.Register(mux)

    // Ваши обычные обработчики
    mux.HandleFunc("/api/health", healthHandler)
    mux.HandleFunc("/api/users", usersHandler)

    http.ListenAndServe(":8080", mux)
}
```

Теперь профилирование доступно по адресу:
`http://localhost:8080/debug/pprof/`

---

## 2. Автономный pprof-сервер (отдельный порт)

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/jwm1rr0rb10/go-pprof"
)

func main() {
    // Можно использовать NewConfig("", 0, 0) — будут применены дефолтные значения
    cfg := pprof.NewConfig("127.0.0.1", 6060, 10*time.Second)
    server := pprof.NewServer(cfg)

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    // Запускаем pprof-сервер в отдельной горутине
    go func() {
        if err := server.Run(ctx); err != nil && err != context.Canceled {
            log.Printf("pprof server error: %v", err)
        }
    }()

    // ... основной код вашего приложения ...

    // При завершении приложения gracefully останавливаем pprof-сервер
    <-someShutdownSignal
    cancel()
}
```

---

## Конфигурация

```go
cfg := pprof.NewConfig(host, port, readHeaderTimeout)

// Значения по умолчанию (если передать пустые):
// Host:              "127.0.0.1"
// Port:              6060
// ReadHeaderTimeout: 10 * time.Second
```

---

## Лучшие практики

- Никогда не выставляйте pprof в интернет — он содержит чувствительную информацию о вашем приложении.
- Запускайте только на localhost или внутри приватной сети/VPC.
- Рекомендуется использовать pprof.Register(mux) вместо отдельного сервера.
- Стандартный порт для pprof — 6060.

---

## Доступные эндпоинты

| Эндпоинт                  | Описание                                                  |
|:--------------------------|:----------------------------------------------------------|
| /debug/pprof/             | Главная страница (индекс)                                 |
| /debug/pprof/profile      | CPU-профиль (30 сек по умолчанию)                         |
| /debug/pprof/heap         | Снимок памяти (heap)                                      |
| /debug/pprof/allocs       | Аллокации объектов                                        |
| /debug/pprof/goroutine    | Стек горутин                                              |
| /debug/pprof/block        | Блокирующие операции                                      |
| /debug/pprof/mutex        | Конкуренция мьютексов                                     |
| /debug/pprof/threadcreate | Созданные потоки                                          |
| /debug/pprof/trace        | Трассировка выполнения                                    |
| /debug/pprof/cmdline      | Командная строка                                          |
| /debug/pprof/symbol       | Символы (для инструментов)                                |

---

## Тестирование

```bash
go test ./...
```

---

## ## License
[MIT License](https://github.com/jwm1rr0rb10/go-pprof/blob/main/LICENSE) – © Raman Zaitsau [@jwm1rrr0rb10](https://github.com/jwm1rr0rb10)

