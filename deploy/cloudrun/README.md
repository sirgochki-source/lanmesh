# lanmesh signal на Cloud Run / Render (бесплатно)

Гоняет Go-сигналку `cmd/lanmesh-signal` в контейнере — **без правок кода**. Сервер
читает порт из `$PORT` (его задаёт платформа), HTTPS терминирует сама платформа.
Состояние в памяти: после холодного старта реестр восстанавливается за ~20с (клиенты
перерегистрируются) — для сигналки это нормально.

Файл: [`Dockerfile`](./Dockerfile) (собирать из **корня репо**, контекст `.`).

## Google Cloud Run (2M запросов/мес, scale-to-zero, домен `*.run.app`)

Самый простой путь — временно положить Dockerfile в корень и деплой из исходников:

```sh
cp deploy/cloudrun/Dockerfile ./Dockerfile
gcloud run deploy lanmesh-signal --source . \
  --region europe-west1 --allow-unauthenticated
rm ./Dockerfile
```

Либо собрать образ явно и задеплоить его:

```sh
gcloud builds submit --tag gcr.io/<PROJECT>/lanmesh-signal \
  --config /dev/stdin <<'EOF'
steps:
- name: gcr.io/cloud-builders/docker
  args: ['build','-f','deploy/cloudrun/Dockerfile','-t','gcr.io/$PROJECT_ID/lanmesh-signal','.']
images: ['gcr.io/$PROJECT_ID/lanmesh-signal']
EOF
gcloud run deploy lanmesh-signal --image gcr.io/<PROJECT>/lanmesh-signal \
  --region europe-west1 --allow-unauthenticated
```

URL появится в выводе: `https://lanmesh-signal-xxxx.run.app`. Проверь `/health`.

## Render (free web service, домен `*.onrender.com`)

New → **Web Service** → подключить репозиторий → Runtime: **Docker**:

- **Dockerfile Path**: `deploy/cloudrun/Dockerfile`
- **Docker Build Context**: `.` (корень)
- **Instance Type**: Free

`$PORT` Render задаёт сам. Free-инстанс засыпает после 15 мин простоя (холодный старт
30–60с), но клиенты lanmesh стучатся каждые 20с — пока сетью пользуются, он не спит.

## Подключение клиента

Добавь выданный URL в пул сигналок (не заменяя другие):

- **Панель** → «Серверы» → впиши `https://…run.app` / `https://….onrender.com`.
- или `config.json` → `"signals": [ …, "https://…" ]`.

Диагностика: `GET https://…/logs?net=<тег>`.

## Заметки

- Порт: код берёт `PORT` из окружения; локально без него слушает `-listen :25556`.
- HTTPS даёт платформа — TLS-флаги (`-tls-*`) не нужны.
- **UDP-ретранслятор так НЕ поднять** — Cloud Run/Render только HTTP. Для релея нужна
  VM (Oracle Always Free ARM, GCP e2-micro и т.п.).
