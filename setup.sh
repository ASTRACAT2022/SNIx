#!/bin/bash

# SNI Proxy Auto-Setup Script для Brawl Stars и других игр
# Запускать от root: sudo ./setup.sh

set -e

echo "🚀 SNI Proxy Auto-Setup"
echo "========================"

# Цвета
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Получить внешний IP
EXTERNAL_IP=$(curl -s ifconfig.me)
echo -e "${YELLOW}Внешний IP: $EXTERNAL_IP${NC}"

# Остановить старые процессы
echo "📋 Остановка старых процессов..."
pkill -9 sni-proxy 2>/dev/null || true
sleep 2

# Очистить iptables
echo "🧹 Очистка iptables..."
iptables -t nat -F PREROUTING 2>/dev/null || true
iptables -F FORWARD 2>/dev/null || true

# Включить IP forwarding
echo "📡 Включение IP forwarding..."
echo 1 > /proc/sys/net/ipv4/ip_forward

# Обновить файлы
echo "📥 Обновление из GitHub..."
git pull

# Скомпилировать
echo "🔨 Компиляция..."
go build -o proxy -ldflags="-s -w" main.go

# Установка systemd сервиса
echo "⚙️ Настройка systemd службы..."

# Получаем текущую директорию (где лежит скрипт и проект)
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Создаем файл сервиса динамически с правильными путями
cat > /etc/systemd/system/sni-proxy.service <<EOF
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

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=sni-proxy

[Install]
WantedBy=multi-user.target
EOF

# Перезагружаем systemd и запускаем
systemctl daemon-reload
systemctl enable sni-proxy
systemctl restart sni-proxy
sleep 3

# Проверить запуск
if systemctl is-active --quiet sni-proxy; then
    echo -e "${GREEN}✅ SNI Proxy запущен как systemd служба!${NC}"
else
    echo -e "${RED}❌ Ошибка запуска службы! Проверьте логи: journalctl -u sni-proxy -e${NC}"
    exit 1
fi

# Подождать настройки iptables
sleep 2

# Показать статус
echo ""
echo "📊 Статус:"
echo "=========="

# Проверить порты
echo "Порты:"
ss -tlnp | grep -E "(9339|443|30000)" || echo "  Нет активных портов"

# Показать логи
echo ""
echo "Последние логи (journalctl):"
journalctl -u sni-proxy -n 10 --no-pager | grep -E "(🎮|TCP|UDP|Listening)" || echo "  Нет логов"

echo ""
echo "========================"
echo -e "${GREEN}✅ Установка завершена! Прокси работает в фоне (systemctl).${NC}"
echo ""
echo "📱 Для подключения игр:"
echo "   1. Установите DNS на телефоне = $EXTERNAL_IP"
echo "   2. Запустите игру (Brawl Stars, Clash Royale, etc.)"
echo ""
echo "📊 Мониторинг логов в реальном времени:"
echo "   journalctl -u sni-proxy -f"
echo ""
echo "🛑 Остановка службы:"
echo "   systemctl stop sni-proxy"
echo ""
