# lanmesh signal на Deno Deploy (бесплатно)

Порт сигналки на **Deno Deploy** + **Deno KV** — ещё одна сигналка в пул, на другом
провайдере и домене (`*.deno.dev`, не `*.workers.dev`). Бесплатный тариф: 1M запросов/мес
(100k/день), 1 GiB KV. Одному-двум десяткам друзей — с большим запасом.

Файл: [`signal.ts`](./signal.ts).

## Локальная проверка

KV локально требует флага `--unstable-kv` (на Deno Deploy он включён сам):

```sh
deno run --unstable-kv --allow-net --allow-read --allow-write deploy/deno/signal.ts
curl localhost:8000/health          # -> lanmesh signal ok
```

## Деплой

> Классический `dash.deno.com` закрывают **20 июля 2026** — заводи проект на
> **console.deno.com**.

**Вариант А — через GitHub (проще):** в console.deno.com → New Project → подключить
этот репозиторий → Entrypoint: `deploy/deno/signal.ts`. Дальше пуши в ветку = авто-деплой.

**Вариант Б — из CLI:**

```sh
deno install -gArf jsr:@deno/deployctl
deployctl deploy --project=<имя-проекта> --prod deploy/deno/signal.ts
```

После деплоя проверь: `curl https://<имя-проекта>.deno.dev/health`.

## Подключение клиента

Добавь URL в пул сигналок (движок ходит во все сразу, ничего не переключая):

- **Панель** → «Серверы» → впиши `https://<имя-проекта>.deno.dev` в список.
- или `%APPDATA%\lanmesh\config.json` → `"signals": [ …, "https://<имя-проекта>.deno.dev" ]`.
- headless: `lanmesh -signal https://<имя-проекта>.deno.dev,<другие…>`.

Диагностика (как у остальных сигналок): `GET https://<имя-проекта>.deno.dev/logs?net=<тег>`.

## Заметки

- Состояние — в Deno KV, переживает выгрузку инстанса. TTL пиров/логов — через
  `expireIn`, протухшее KV удаляет само.
- HTTPS даёт сама платформа — сертификаты настраивать не нужно.
- Тот же протокол, что у воркера и `cmd/lanmesh-signal`: `/register`, `/log`, `/logs`, `/health`.
