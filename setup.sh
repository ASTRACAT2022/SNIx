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
go build -o sni-proxy -ldflags="-s -w" main.go

# Запустить
echo "🚀 Запуск SNI Proxy..."
nohup ./sni-proxy config.json > /dev/null 2>&1 &
sleep 3

# Проверить запуск
if pgrep -x "sni-proxy" > /dev/null; then
    echo -e "${GREEN}✅ SNI Proxy запущен!${NC}"
else
    echo -e "${RED}❌ Ошибка запуска!${NC}"
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

# Проверить iptables
echo ""
echo "iptables правила:"
iptables -t nat -L PREROUTING -n -v | grep 9339 || echo "  Нет правил для 9339"

# Показать логи
echo ""
echo "Последние логи:"
tail -20 logs/proxy.log | grep -E "(🎮|TCP|UDP|Listening)" || echo "  Нет логов"

echo ""
echo "========================"
echo -e "${GREEN}✅ Готово!${NC}"
echo ""
echo "📱 Для подключения игр:"
echo "   1. Установите DNS на телефоне = $EXTERNAL_IP"
echo "   2. Запустите игру (Brawl Stars, Clash Royale, etc.)"
echo ""
echo "📊 Мониторинг логов:"
echo "   tail -f logs/proxy.log | grep -E '(🎮|TCP|9339|brawl)'"
echo ""
echo "🛑 Остановка:"
echo "   pkill sni-proxy"
echo ""
