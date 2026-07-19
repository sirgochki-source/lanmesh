# lanmesh signal для Cloud Run / Render / Railway и любого контейнер-хостинга.
#
# Гоняет cmd/lanmesh-signal как есть; состояние в памяти (реестр восстанавливается
# сам за один цикл регистрации ~20с после холодного старта). Порт берём из $PORT,
# который платформа пробрасывает; HTTPS терминирует сама платформа — свой TLS не нужен.
#
# Собирать из КОРНЯ репозитория:
#   docker build -f deploy/cloudrun/Dockerfile -t lanmesh-signal .
# Cloud Run:  gcloud run deploy lanmesh-signal --source . --region <...> --allow-unauthenticated
#   (если Cloud Run не находит Dockerfile в корне — укажи его: --dockerfile deploy/cloudrun/Dockerfile,
#    либо скопируй этот файл в корень как Dockerfile).

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/lanmesh-signal ./cmd/lanmesh-signal

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lanmesh-signal /lanmesh-signal
# Cloud Run/Render задают $PORT сами; 8080 — дефолт на случай локального запуска.
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/lanmesh-signal"]
