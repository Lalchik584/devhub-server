#!/bin/bash
set -e

# ══════════════════════════════════════════════════════════════
#  DevHub Server Installer v2.0
#  Автоматическая установка + автозапуск через systemd
# ══════════════════════════════════════════════════════════════

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log_info()  { echo -e "${GREEN}✓${NC} $1"; }
log_warn()  { echo -e "${YELLOW}⚠${NC} $1"; }
log_error() { echo -e "${RED}✗${NC} $1"; }
log_step()  { echo -e "\n${CYAN}▶ $1${NC}"; }

INSTALL_DIR="/opt/devhub"
REPO_URL="https://github.com/your-name/devhub-server.git"

# ══════════════════════════════════════════════════════════════
# 1. ПРОВЕРКИ СИСТЕМЫ
# ══════════════════════════════════════════════════════════════

echo -e "${CYAN}"
echo "╔═══════════════════════════════════════════════╗"
echo "║      DevHub Server Installer v2.0             ║"
echo "╚═══════════════════════════════════════════════╝"
echo -e "${NC}"

log_step "Проверка системы"

# Root
if [ "$EUID" -ne 0 ]; then
    log_error "Запустите от root: sudo bash install.sh"
    exit 1
fi

# ОС
if [ -f /etc/os-release ]; then
    . /etc/os-release
    if [[ ! "$ID" =~ ^(ubuntu|debian)$ ]]; then
        log_warn "Поддерживаются Ubuntu/Debian (у вас $ID). Продолжить?"
        read -p "[y/N]: " cont
        [[ ! "$cont" =~ ^[Yy]$ ]] && exit 1
    fi
    log_info "ОС: $PRETTY_NAME"
fi

# Интернет
if ! curl -s --max-time 5 https://google.com > /dev/null; then
    log_error "Нет доступа в интернет"
    exit 1
fi
log_info "Интернет доступен"

# Место на диске
AVAILABLE=$(df / | tail -1 | awk '{print $4}')
if [ "$AVAILABLE" -lt 10485760 ]; then
    log_error "Мало места (нужно ≥10 ГБ)"
    exit 1
fi
log_info "Свободно: $((AVAILABLE / 1024 / 1024)) ГБ"

# ══════════════════════════════════════════════════════════════
# 2. ОПРЕДЕЛЕНИЕ IP
# ══════════════════════════════════════════════════════════════

log_step "Определение IP-адресов"

LOCAL_IP=$(hostname -I | awk '{print $1}')
PUBLIC_IP=$(curl -s --max-time 5 ifconfig.me || echo "")

log_info "Локальный IP: $LOCAL_IP"
[ -n "$PUBLIC_IP" ] && log_info "Внешний IP:   $PUBLIC_IP" || log_warn "Не удалось определить внешний IP"

echo ""
read -p "Использовать внешний IP для основного доступа? [Y/n]: " use_public
if [[ "$use_public" =~ ^[Nn]$ ]] || [ -z "$PUBLIC_IP" ]; then
    SERVER_IP=$LOCAL_IP
else
    SERVER_IP=$PUBLIC_IP
fi
log_info "Основной адрес: $SERVER_IP"

# ══════════════════════════════════════════════════════════════
# 3. ПРОВЕРКА СУЩЕСТВУЮЩИХ СЛУЖБ
# ══════════════════════════════════════════════════════════════

log_step "Проверка существующих служб"

# Docker
DOCKER_INSTALLED=false
if command -v docker &> /dev/null; then
    log_info "Docker уже установлен ($(docker --version | awk '{print $3}' | tr -d ','))"
    DOCKER_INSTALLED=true
else
    log_warn "Docker не найден"
    read -p "Установить Docker? [Y/n]: " install_docker
    if [[ ! "$install_docker" =~ ^[Nn]$ ]]; then
        curl -fsSL https://get.docker.com | bash
        systemctl enable docker
        systemctl start docker
        log_info "Docker установлен"
        DOCKER_INSTALLED=true
    else
        log_error "Без Docker установка невозможна"
        exit 1
    fi
fi

# Docker Compose
if ! docker compose version &> /dev/null; then
    log_info "Установка Docker Compose..."
    apt-get update -qq && apt-get install -y -qq docker-compose-plugin
fi
log_info "Docker Compose готов"

# Go
GO_INSTALLED=false
if command -v go &> /dev/null; then
    log_info "Go уже установлен ($(go version | awk '{print $3}'))"
    GO_INSTALLED=true
else
    log_warn "Go не найден"
    read -p "Установить Go для сборки Engine? [Y/n]: " install_go
    if [[ ! "$install_go" =~ ^[Nn]$ ]]; then
        ARCH=$(uname -m)
        case $ARCH in
            x86_64) GO_ARCH="amd64" ;;
            aarch64) GO_ARCH="arm64" ;;
            *) GO_ARCH="amd64" ;;
        esac
        wget -q "https://go.dev/dl/go1.22.2.linux-${GO_ARCH}.tar.gz" -O /tmp/go.tar.gz
        tar -C /usr/local -xzf /tmp/go.tar.gz
        export PATH=$PATH:/usr/local/go/bin
        echo 'export PATH=$PATH:/usr/local/go/bin' >> /root/.bashrc
        rm /tmp/go.tar.gz
        log_info "Go установлен"
        GO_INSTALLED=true
    fi
fi

# Порты
log_step "Проверка портов"
PORT_CONFLICT=false
for port in 3000 5050 5051 80 443; do
    if ss -tlnp 2>/dev/null | grep -q ":$port "; then
        log_warn "Порт $port занят"
        PORT_CONFLICT=true
    fi
done

if [ "$PORT_CONFLICT" = true ]; then
    read -p "Есть конфликты портов. Продолжить? [y/N]: " cont
    [[ ! "$cont" =~ ^[Yy]$ ]] && exit 1
fi

# Gitea
if docker ps -a --format '{{.Names}}' | grep -q "^gitea$"; then
    log_warn "Контейнер gitea уже существует"
    read -p "Удалить старый контейнер? [y/N]: " rm_gitea
    if [[ "$rm_gitea" =~ ^[Yy]$ ]]; then
        docker rm -f gitea
        log_info "Старый контейнер удалён"
    fi
fi

# ══════════════════════════════════════════════════════════════
# 4. FCM (ОПЦИОНАЛЬНО)
# ══════════════════════════════════════════════════════════════

log_step "Push-уведомления (FCM)"

FCM_ENABLED=false
read -p "Настроить push-уведомления? [y/N]: " setup_fcm
if [[ "$setup_fcm" =~ ^[Yy]$ ]]; then
    read -p "Путь к fcm-key.json: " fcm_path
    if [ -f "$fcm_path" ]; then
        mkdir -p $INSTALL_DIR/engine
        cp "$fcm_path" $INSTALL_DIR/engine/fcm-key.json
        FCM_ENABLED=true
        log_info "FCM настроен"
    else
        log_warn "Файл не найден, FCM пропущен"
    fi
else
    log_info "FCM пропущен (можно настроить позже)"
fi

# ══════════════════════════════════════════════════════════════
# 5. СКАЧИВАНИЕ DEVHUB
# ══════════════════════════════════════════════════════════════

log_step "Скачивание DevHub"

mkdir -p $INSTALL_DIR
cd $INSTALL_DIR

if [ -d "$INSTALL_DIR/.git" ]; then
    log_info "Обновление существующей копии"
    git pull
else
    if [ -n "$REPO_URL" ] && [ "$REPO_URL" != "https://github.com/your-name/devhub-server.git" ]; then
        git clone $REPO_URL .
    else
        log_warn "Репозиторий не настроен — используется локальная копия"
    fi
fi

# ══════════════════════════════════════════════════════════════
# 6. ПОДСТАНОВКА IP В КОНФИГИ
# ══════════════════════════════════════════════════════════════

log_step "Настройка конфигурации"

if [ -f "$INSTALL_DIR/docker-compose.yml.template" ]; then
    sed "s/{{SERVER_IP}}/$SERVER_IP/g; s/{{LOCAL_IP}}/$LOCAL_IP/g; s/{{PUBLIC_IP}}/$PUBLIC_IP/g" \
        $INSTALL_DIR/docker-compose.yml.template > $INSTALL_DIR/docker-compose.yml
    log_info "docker-compose.yml создан"
fi

mkdir -p $INSTALL_DIR/{gitea/data,mosquitto/config,mosquitto/data,projects,snapshots,engine,stats}

# Mosquitto конфиг
cat > $INSTALL_DIR/mosquitto/config/mosquitto.conf << 'MQTT'
listener 1883
allow_anonymous true
persistence true
persistence_location /mosquitto/data/
log_dest stdout
MQTT

# ══════════════════════════════════════════════════════════════
# 7. СБОРКА ENGINE
# ══════════════════════════════════════════════════════════════

log_step "Сборка DevHub Engine"

if [ -f "$INSTALL_DIR/engine/main.go" ]; then
    cd $INSTALL_DIR/engine
    CGO_ENABLED=0 go build -o devhub-engine main.go
    log_info "Engine скомпилирован"

    cat > Dockerfile << 'DOCKERFILE'
FROM alpine:latest
RUN apk add --no-cache docker-cli python3 py3-pip
RUN pip install --break-system-packages google-auth requests 2>/dev/null || true
WORKDIR /app
COPY devhub-engine .
COPY fcm-key.json* /app/ 2>/dev/null || true
COPY send_fcm.py* /app/ 2>/dev/null || true
EXPOSE 5050
CMD ["./devhub-engine"]
DOCKERFILE

    docker build -t devhub-engine:local .
    log_info "Образ Engine собран"
fi

# ══════════════════════════════════════════════════════════════
# 8. ЗАПУСК ВСЕХ СЕРВИСОВ
# ══════════════════════════════════════════════════════════════

log_step "Запуск сервисов"

docker rm -f devhub-engine stats-server gitea mosquitto 2>/dev/null || true

cd $INSTALL_DIR
if [ -f "docker-compose.yml" ]; then
    docker compose up -d
else
    log_error "docker-compose.yml не найден"
    exit 1
fi

sleep 5

# ══════════════════════════════════════════════════════════════
# 9. СИСТЕМНЫЕ СЕРВИСЫ SYSTEMD (АВТОЗАПУСК)
# ══════════════════════════════════════════════════════════════

log_step "Настройка автозапуска через systemd"

# ─── Stats Server ───
if [ -f "$INSTALL_DIR/stats/stats_server.go" ]; then
    cat > /etc/systemd/system/devhub-stats.service << EOF
[Unit]
Description=DevHub Stats Server
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR/stats
ExecStart=/usr/local/go/bin/go run stats_server.go
Restart=always
RestartSec=5
StandardOutput=append:/var/log/devhub-stats.log
StandardError=append:/var/log/devhub-stats.log

[Install]
WantedBy=multi-user.target
EOF
    log_info "Служба devhub-stats создана"
fi

# ─── Docker Compose (Gitea, Mosquitto, Engine) ───
cat > /etc/systemd/system/devhub.service << EOF
[Unit]
Description=DevHub Docker Compose Stack
Requires=docker.service
After=docker.service network.target
Wants=devhub-stats.service

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=$INSTALL_DIR
ExecStart=/usr/bin/docker compose up -d
ExecStop=/usr/bin/docker compose down
TimeoutStartSec=300

[Install]
WantedBy=multi-user.target
EOF
log_info "Служба devhub создана"

# ─── Автозапуск Docker ───
systemctl enable docker

# ─── Активация ───
systemctl daemon-reload
systemctl enable devhub.service
systemctl enable devhub-stats.service
systemctl start devhub-stats.service 2>/dev/null || true

log_info "Автозапуск настроен"

# ══════════════════════════════════════════════════════════════
# 10. FIREWALL
# ══════════════════════════════════════════════════════════════

log_step "Настройка файрвола"

if command -v ufw &> /dev/null; then
    ufw allow 22/tcp 2>/dev/null || true
    ufw allow 3000/tcp 2>/dev/null || true
    ufw allow 5050/tcp 2>/dev/null || true
    ufw allow 5051/tcp 2>/dev/null || true
    log_info "Порты открыты в ufw"
fi

# ══════════════════════════════════════════════════════════════
# 11. ПРОВЕРКА РАБОТОСПОСОБНОСТИ
# ══════════════════════════════════════════════════════════════

log_step "Проверка работы"

sleep 3

# Gitea
if curl -s --max-time 5 "http://localhost:3000" > /dev/null; then
    log_info "Gitea работает"
else
    log_warn "Gitea ещё запускается (может занять 30 секунд)"
fi

# Engine
if curl -s --max-time 5 "http://localhost:5050/api/health" > /dev/null; then
    log_info "Engine работает"
else
    log_warn "Engine ещё запускается"
fi

# Stats
if curl -s --max-time 5 "http://localhost:5051/api/system/stats" > /dev/null; then
    log_info "Stats работает"
else
    log_warn "Stats ещё запускается"
fi

# ══════════════════════════════════════════════════════════════
# 12. ФИНАЛЬНЫЙ ВЫВОД
# ══════════════════════════════════════════════════════════════

echo ""
echo -e "${GREEN}╔═══════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║        УСТАНОВКА ЗАВЕРШЕНА 🎉                 ║${NC}"
echo -e "${GREEN}╚═══════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${CYAN}📡 АДРЕСА ДЛЯ ПРИЛОЖЕНИЯ:${NC}"
echo -e "   Локальный:  ${GREEN}http://$LOCAL_IP:3000${NC}"
[ -n "$PUBLIC_IP" ] && echo -e "   Внешний:    ${GREEN}http://$PUBLIC_IP:3000${NC}"
echo ""
echo -e "${CYAN}🌐 СЕРВИСЫ:${NC}"
echo -e "   Gitea:      ${GREEN}http://$SERVER_IP:3000${NC}"
echo -e "   Engine:     ${GREEN}http://$SERVER_IP:5050/api/health${NC}"
echo -e "   Stats:      ${GREEN}http://$SERVER_IP:5051/api/system/stats${NC}"
echo ""
echo -e "${CYAN}🔧 СТАТУС:${NC}"
echo -e "   Docker:     ${GREEN}✓${NC}"
echo -e "   Go:         $( [ "$GO_INSTALLED" = true ] && echo "${GREEN}✓${NC}" || echo "${YELLOW}не установлен${NC}" )"
echo -e "   FCM:        $( [ "$FCM_ENABLED" = true ] && echo "${GREEN}✓ настроен${NC}" || echo "${YELLOW}не настроен${NC}" )"
echo -e "   Автозапуск: ${GREEN}✓ systemd${NC}"
echo ""
echo -e "${CYAN}🔁 АВТОЗАПУСК:${NC}"
echo "   При перезагрузке сервера всё поднимется само."
echo "   Проверить: systemctl status devhub"
echo ""
echo -e "${CYAN}📱 СЛЕДУЮЩИЙ ШАГ:${NC}"
echo "   1. Откройте Gitea и зарегистрируйтесь: http://$SERVER_IP:3000"
echo "   2. Установите приложение DevHub на телефон"
echo "   3. Введите оба адреса (локальный и внешний)"
echo ""
