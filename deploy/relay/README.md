# lanmesh relay на бесплатной VM

Ретранслятор (`cmd/lanmesh-relay`) нужен для пар, у кого не пробивается прямое
соединение (симметричный NAT / мобильный CGNAT). Требует **реальную VM с публичным
IP и открытым UDP** — serverless не подходит (UDP не умеет). Стоять он должен **не на
LAN игрока**: домашний релей за тем же роутером, что и ПК, до этого ПК не доставит
(hairpin).

Бинарь тупой и трафик не расшифровывает (ключа сети у него нет), состояние в памяти,
слушает **UDP :25555**. Одной free-VM хватает и на релей, и на свою сигналку.

Бинари (собери из корня или возьми из релиза):
```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o lanmesh-relay ./cmd/lanmesh-relay  # Oracle ARM / RPi
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o lanmesh-relay ./cmd/lanmesh-relay  # GCP e2-micro / обычный VPS
```

## Где взять бесплатную VM

- **Oracle Cloud Always Free (ARM Ampere)** — 24/7, 2 OCPU/12 GB, UDP разрешён.
  Лучший бесплатный «навсегда». Минус: ARM-ёмкость в популярных регионах часто
  «out of host capacity» — пробуй разные регионы/зоны. Бинарь **arm64**.
- **Google Cloud e2-micro Always Free** — 1 VM (us-west1/us-east1/us-central1),
  1 GB RAM. Проще получить, чем Oracle ARM. Бинарь **amd64**.
- Любой VPS с белым IP — бинарь под его архитектуру.

## Установка (systemd)

```sh
# 1) залить бинарь на VM
scp lanmesh-relay user@<vm>:/tmp/
ssh user@<vm>
sudo mv /tmp/lanmesh-relay /usr/local/bin/ && sudo chmod +x /usr/local/bin/lanmesh-relay

# 2) юнит (запуск от nobody, ничего не хранит на диске)
sudo tee /etc/systemd/system/lanmesh-relay.service >/dev/null <<'EOF'
[Unit]
Description=lanmesh relay
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/lanmesh-relay -listen :25555
DynamicUser=yes
Restart=always
RestartSec=2
# только исходящий/входящий UDP нужен; никаких прав и файлов
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes

[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now lanmesh-relay
systemctl status lanmesh-relay --no-pager
```

## Открыть UDP 25555 (ОБА уровня!)

**1. Облачный фаервол / security group:**
- **Oracle:** VCN → Security Lists → Ingress Rule: Source `0.0.0.0/0`, IP Protocol UDP,
  Destination Port `25555`.
- **GCP:** `gcloud compute firewall-rules create lanmesh-relay --allow udp:25555 --source-ranges 0.0.0.0/0`

**2. Фаервол ОС на самой VM** (у Oracle-образов iptables по умолчанию DROP!):
```sh
# iptables (Oracle Ubuntu/Oracle Linux)
sudo iptables -I INPUT -p udp --dport 25555 -j ACCEPT
sudo netfilter-persistent save      # чтобы пережило перезагрузку (или iptables-save)
# ИЛИ firewalld:
sudo firewall-cmd --permanent --add-port=25555/udp && sudo firewall-cmd --reload
# ИЛИ ufw:
sudo ufw allow 25555/udp
```

## Проверка

С другой машины (`nc`/`ncat` UDP не подтвердит из-за отсутствия ответа на мусор — это
норма). Надёжнее — по логу релея: `journalctl -u lanmesh-relay -f`, там появятся
строки `bind …` когда клиент пропишется.

## Подключение клиента

В панели: **«Отключиться» → «Серверы» → строка `ретранслятор (relay)` = `<публичный-IP>:25555`
→ Сохранить → «Подключиться»**. Или `config.json` → `"relay": "<IP>:25555"`.

Relay в lanmesh один на узел (не список). Он вступает в игру только когда прямое
пробитие не удалось за несколько секунд — прямой путь всегда предпочтительнее.
