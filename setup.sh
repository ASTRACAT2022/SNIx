#!/bin/bash

# SNI Proxy Auto-Setup Script
# Запускать от root: sudo ./setup.sh

set -e

echo "🚀 SNI Proxy Auto-Setup"
echo "========================"
echo ""

# Цвета
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Получить IPv4
IPv4=$(curl -s -4 ifconfig.me 2>/dev/null || echo "")
if [ -z "$IPv4" ]; then
    IPv4=$(curl -s -4 ipinfo.io/ip 2>/dev/null || echo "Не удалось определить")
fi
echo -e "${YELLOW}Внешний IPv4: $IPv4${NC}"

# Если есть IPv6 тоже покажем
IPv6=$(curl -s -6 ifconfig.me 2>/dev/null || echo "")
if [ -n "$IPv6" ]; then
    echo -e "${YELLOW}Внешний IPv6: $IPv6${NC}"
fi
echo ""

# ========== ВОПРОСЫ ==========

# mo.co
echo "Нужен ли прокси для игры mo.co (Squad Busters)? [y/n]"
read moco_choice
echo ""

# Supercell
echo "Нужен ли прокси для одной из игр Supercell?"
echo "Все Supercell игры, кроме mo.co, используют порт 9339."
echo "На одном сервере может быть только ОДИН прокси для Supercell игр."
echo ""
echo "1. Не нужен"
echo "2. Clash Royale"
echo "3. Clash of Clans"
echo "4. Brawl Stars"
echo "5. Squad Busters (но mo.co это уже выше)"
echo "[1-5]:"
read supercell_choice
echo ""

# Парсим выбор
GAME_SELECTED=""
GAME_DOMAIN=""
GAME_PORT=""

case "$supercell_choice" in
    2)
        GAME_SELECTED="Clash Royale"
        GAME_DOMAIN="game.clashroyaleapp.com"
        GAME_PORT="9339"
        ;;
    3)
        GAME_SELECTED="Clash of Clans"
        GAME_DOMAIN="gamea.clashofclans.com"
        GAME_PORT="9339"
        ;;
    4)
        GAME_SELECTED="Brawl Stars"
        GAME_DOMAIN="game.brawlstarsgame.com"
        GAME_PORT="9339"
        ;;
    5)
        GAME_SELECTED="Squad Busters"
        GAME_DOMAIN="game.squadbustersgame.com"
        GAME_PORT="9339"
        ;;
    *)
        GAME_SELECTED=""
        ;;
esac

# Проверяем что что-то выбрано
if [[ ! "$moco_choice" =~ ^[yY]$ ]] && [ -z "$GAME_SELECTED" ]; then
    echo -e "${RED}❌ Вы ничего не выбрали! Прокси не запущен.${NC}"
    exit 0
fi

echo -e "${GREEN}✅ Выбрано:${NC}"
[[ "$moco_choice" =~ ^[yY]$ ]] && echo "   - mo.co / Squad Busters (порт 30000)"
[ -n "$GAME_SELECTED" ] && echo "   - $GAME_SELECTED (порт $GAME_PORT)"
echo ""

# ========== ОСТАНОВКА СТАРЫХ ПРОЦЕССОВ ==========
echo "📋 Остановка старых процессов..."
pkill -9 sni-proxy 2>/dev/null || true
pkill -9 proxy 2>/dev/null || true
sleep 2

# Очистка iptables
echo "🧹 Очистка iptables..."
iptables -t nat -F PREROUTING 2>/dev/null || true
iptables -F FORWARD 2>/dev/null || true

# Включить IP forwarding
echo "📡 Включение IP forwarding..."
echo 1 > /proc/sys/net/ipv4/ip_forward 2>/dev/null || true

# ========== ОБНОВЛЕНИЕ КОДА ==========
echo "📥 Обновление из GitHub..."
git pull 2>/dev/null || echo -e "${YELLOW}⚠️ Git pull пропущен${NC}"

# ========== КОМПИЛЯЦИЯ ==========
echo "🔨 Компиляция..."
go build -o proxy -ldflags="-s -w" main.go
if [ $? -ne 0 ]; then
    echo -e "${RED}❌ Ошибка компиляции!${NC}"
    exit 1
fi

# ========== ОБНОВЛЕНИЕ CONFIG.JSON ==========
echo "⚙️ Обновление config.json..."

# Определяем какие порты слушать
# Всегда слушаем 80 и 443
# Добавляем игровые порты
GAME_PORTS=""
[[ "$moco_choice" =~ ^[yY]$ ]] && GAME_PORTS="${GAME_PORTS}30000, "
[ -n "$GAME_PORT" ] && GAME_PORTS="${GAME_PORTS}${GAME_PORT}, "

# Убираем последнюю запятую
GAME_PORTS="${GAME_PORTS%, }"

# Обновляем listen_ports в config.json
if grep -q '"listen_ports"' config.json; then
    sed -i "s/\"listen_ports\": \[.*\]/\"listen_ports\": [80, 443, ${GAME_PORTS}]/" config.json
fi

# ========== ОБНОВЛЕНИЕ main.go ==========
echo "⚙️ Обновление main.go..."

# Резервная копия
cp main.go main.go.bak 2>/dev/null || true

# Обновляем маршрутизацию в main.go для порта 9339
if [ -n "$GAME_DOMAIN" ]; then
    # Меняем домен для порта 9339 в зависимости от выбора
    case "$supercell_choice" in
        2)
            sed -i 's/targetDomain = "game.brawlstarsgame.com"/targetDomain = "game.clashroyaleapp.com"/' main.go
            ;;
        3)
            sed -i 's/targetDomain = "game.brawlstarsgame.com"/targetDomain = "gamea.clashofclans.com"/' main.go
            ;;
        4)
            # По умолчанию и так Brawl Stars
            ;;
        5)
            sed -i 's/targetDomain = "game.brawlstarsgame.com"/targetDomain = "game.squadbustersgame.com"/' main.go
            ;;
    esac
    echo "   Порт 9339 -> $GAME_DOMAIN"
fi

if [[ "$moco_choice" =~ ^[yY]$ ]]; then
    echo "   Порт 30000 -> game.mocogame.com"
fi

# Перекомпилируем
echo "🔨 Перекомпиляция с новыми настройками..."
go build -o proxy -ldflags="-s -w" main.go
if [ $? -ne 0 ]; then
    echo -e "${RED}❌ Ошибка компиляции после обновления!${NC}"
    echo "Восстанавливаем оригинальный main.go..."
    mv main.go.bak main.go 2>/dev/null || true
    exit 1
fi

# Удаляем резервную копию
rm -f main.go.bak

# ========== SYSTEMD СЕРВИС ==========
echo "⚙️ Настройка systemd службы..."

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

cat > /etc/systemd/system/sni-proxy.service << EOF
[Unit]
Description=Game SNI Proxy
After=network.target
Wants=network.target

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=${DIR}
ExecStart=${DIR}/proxy ${DIR}/config.json
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

StandardOutput=journal
StandardError=journal
SyslogIdentifier=sni-proxy

[Install]
WantedBy=multi-user.target
EOF

# Перезагрузка systemd
systemctl daemon-reload
systemctl enable sni-proxy
systemctl restart sni-proxy
sleep 3

# ========== ПРОВЕРКА ==========
echo ""
echo "📊 Статус:"
echo "=========="

if systemctl is-active --quiet sni-proxy; then
    echo -e "${GREEN}✅ SNI Proxy запущен!${NC}"
else
    echo -e "${RED}❌ Ошибка запуска!${NC}"
    echo "Логи:"
    journalctl -u sni-proxy -n 20 --no-pager
    exit 1
fi

echo ""
echo "Активные порты:"
ss -tlnp 2>/dev/null | grep -E "(9339|9340|9341|30000|443|80)" || echo "  Не удалось определить"

echo ""
echo "Последние логи:"
journalctl -u sni-proxy -n 10 --no-pager 2>/dev/null | grep -E "(Listening|Game|🎮)" || echo "  Нет логов"

echo ""
echo "========================"
echo -e "${GREEN}✅ Установка завершена!${NC}"
echo ""
echo -e "${BLUE}📱 Для подключения:${NC}"

if [ -n "$IPv4" ] && [ "$IPv4" != "Не удалось определить" ]; then
    [[ "$moco_choice" =~ ^[yY]$ ]] && echo "   mo.co -> DNS: $IPv4 (порт 30000)"
    [ -n "$GAME_SELECTED" ] && echo "   $GAME_SELECTED -> DNS: $IPv4 (порт $GAME_PORT)"
fi

echo ""
echo "📊 Мониторинг:"
echo "   journalctl -u sni-proxy -f"
echo ""
echo "🛑 Управление:"
echo "   systemctl stop sni-proxy"
echo "   systemctl start sni-proxy"
echo "   systemctl restart sni-proxy"
echo ""