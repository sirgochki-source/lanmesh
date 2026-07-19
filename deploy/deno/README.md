# lanmesh signal на Deno Deploy (бесплатно)

Ещё одна сигналка в пул, на другом провайдере и домене (`*.deno.net`, не
`*.workers.dev`). У части провайдеров DPI режет `workers.dev` по имени в ClientHello —
разнородный пул это обходит (движок ходит во все сигналки сразу и сливает списки).

Файл: [`main.ts`](./main.ts). Состояние **в памяти** (как у Go-сигналки
`cmd/lanmesh-signal`) — новый Deno Deploy контейнерный и Deno KV не даёт, а сигналке
персистентность не нужна: пиры перерегистрируются каждые 20с. Бесплатный тариф:
1M запросов/мес — компании друзей с запасом.

## Важно: файл сервера обязан называться `main.ts`

Новый Deno Deploy определяет серверный файл по имени-соглашению (`main.ts`). Флаг
`--entrypoint` в `deno deploy` — это про «динамическую конфигурацию», НЕ про сервер.
Файл с другим именем (`signal.ts` и т.п.) платформа за сервер не признаёт, и сборка
падает на шаге `building` за ~1с. Поэтому здесь именно `main.ts`.

## Локальная проверка

```sh
deno run --allow-net deploy/deno/main.ts
curl --noproxy '*' http://127.0.0.1:8000/health   # -> lanmesh signal ok
```

## Деплой (новый Deno Deploy, console.deno.com)

Деплоит встроенная в Deno команда `deno deploy` (не старый `deployctl` — тот стучится
в классический dash.deno.com, который закрывают 20 июля 2026).

```sh
# токен: console.deno.com -> Settings -> Access Tokens
export DENO_DEPLOY_TOKEN=ddo_...

cd deploy/deno
# создать приложение (один раз):
deno deploy create . --org <твоя-орг> --app lanmesh-signal-deno \
  --source local --runtime-mode dynamic --entrypoint main.ts --region eu \
  --json --non-interactive
# последующие деплои — просто (deno.jsonc помнит org/app):
deno deploy . --prod --json --non-interactive
```

Проверка: `curl https://<app>.<org>.deno.net/health`.

## Подключение клиента

Добавь выданный URL в пул сигналок (движок ходит во все сразу, ничего не переключая):

- **Панель** → «Серверы» → впиши `https://<app>.<org>.deno.net`.
- или `%APPDATA%\lanmesh\config.json` → `"signals": [ …, "https://…deno.net" ]`.
- headless: `lanmesh -signal https://…deno.net,<другие…>`.

Диагностика (как у остальных): `GET https://<app>.<org>.deno.net/logs?net=<тег>`.

## Заметки

- Тот же протокол, что у воркера и `cmd/lanmesh-signal`: `/register`, `/log`, `/logs`, `/health`.
- HTTPS даёт сама платформа — сертификаты настраивать не нужно.
- Если платформа поднимет НЕСКОЛЬКО инстансов, память между ними не общая (для
  низкого трафика обычно один инстанс) — потому в пуле держим ещё воркер/свой сервер.
