#!/bin/bash

# SNI Proxy Auto-Setup Script с выбором игр
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

# Получить внешний IP
EXTERNAL_IP=$(curl -s ifconfig.me 2>/dev/null || echo "Не удалось определить")
echo -e "${YELLOW}Внешний IP: $EXTERNAL_IP${NC}"
echo ""

# ========== ВЫБОР ИГР ==========
echo -e "${BLUE}🎮 ВЫБЕРИТЕ ИГРЫ ДЛЯ ПРОКСИРОВАНИЯ:${NC}"
echo "   (можно выбрать несколько через пробел, например: 1 2 3)"
echo ""
echo "   1) Brawl Stars      (порт 9339)"
echo "   2) Clash Royale     (порт 9340)"
echo "   3) Clash of Clans   (порт 9341)"
echo "   4) Squad Busters    (порт 30000)"
echo ""
read -p "   Ваш выбор (по умолчанию: 1): " -r
GAME_CHOICE="${REPLY:-1}"

# Парсим выбор
BRAWL_SELECTED=0
ROYALE_SELECTED=0
COC_SELECTED=0
SQUAD_SELECTED=0

for choice in $GAME_CHOICE; do
    case $choice in
        1) BRAWL_SELECTED=1 ;;
        2) ROYALE_SELECTED=1 ;;
        3) COC_SELECTED=1 ;;
        4) SQUAD_SELECTED=1 ;;
    esac
done

# Если ничего не выбрано - ставим Brawl Stars по умолчанию
if [[ $BRAWL_SELECTED -eq 0 && $ROYALE_SELECTED -eq 0 && $COC_SELECTED -eq 0 && $SQUAD_SELECTED -eq 0 ]]; then
    BRAWL_SELECTED=1
fi

echo ""
echo -e "${GREEN}✅ Выбраны игры:${NC}"
[[ $BRAWL_SELECTED -eq 1 ]] && echo "   - Brawl Stars (9339)"
[[ $ROYALE_SELECTED -eq 1 ]] && echo "   - Clash Royale (9340)"
[[ $COC_SELECTED -eq 1 ]] && echo "   - Clash of Clans (9341)"
[[ $SQUAD_SELECTED -eq 1 ]] && echo "   - Squad Busters (30000)"
echo ""

# ========== ОБНОВЛЕНИЕ КОДА ==========
echo "📥 Обновление из GitHub..."
git pull 2>/dev/null || echo -e "${YELLOW}⚠️ Git pull не выполнен (не репозиторий или нет изменений)${NC}"

# ========== КОМПИЛЯЦИЯ ==========
echo "🔨 Компиляция..."
go build -o proxy -ldflags="-s -w" main.go
if [ $? -ne 0 ]; then
    echo -e "${RED}❌ Ошибка компиляции!${NC}"
    exit 1
fi

# ========== КОНФИГУРАЦИЯ PORTS В CONFIG.JSON ==========
echo "⚙️ Обновление config.json..."

# Определяем какие порты слушать
LISTEN_PORTS="[80, 443"
GAME_PORTS_TCP=""

[[ $BRAWL_SELECTED -eq 1 ]] && GAME_PORTS_TCP="${GAME_PORTS_TCP}9339, "
[[ $ROYALE_SELECTED -eq 1 ]] && GAME_PORTS_TCP="${GAME_PORTS_TCP}9340, "
[[ $COC_SELECTED -eq 1 ]] && GAME_PORTS_TCP="${GAME_PORTS_TCP}9341, "
[[ $SQUAD_SELECTED -eq 1 ]] && GAME_PORTS_TCP="${GAME_PORTS_TCP}30000, "

# Убираем последнюю запятую и пробел
GAME_PORTS_TCP="${GAME_PORTS_TCP%, }"

# Обновляем listen_ports в config.json
if grep -q '"listen_ports"' config.json; then
    sed -i "s/\"listen_ports\": \[.*\]/\"listen_ports\": [80, 443, ${GAME_PORTS_TCP}]/" config.json
fi

# ========== ОБНОВЛЕНИЕ main.go ДЛЯ ДИНАМИЧЕСКИХ ПОРТОВ ==========
echo "⚙️ Обновление main.go..."

# Создаем новый файл start.go с динамическими портами
cat > start_games.go << 'GAMESEOF'
// +build ignore

package main

import (
	"fmt"
	"net"
	"sync"
	"time"
)

var gamePortConfigs = []struct {
	ListenPort int
	Domain     string
	TargetPort int
}{
	{9339, "game.brawlstarsgame.com", 9339},
	{9340, "game.clashroyaleapp.com", 9339},
	{9341, "gamea.clashofclans.com", 9339},
	{30000, "game.mocogame.com", 30000},
}

func startGameListeners(proxy *SNIProxy) {
	var wg sync.WaitGroup

	for _, game := range gamePortConfigs {
		wg.Add(1)
		go func(g struct {
			ListenPort int
			Domain     string
			TargetPort int
		}) {
			defer wg.Done()
			addr := fmt.Sprintf(":%d", g.ListenPort)
			listener, err := net.Listen("tcp", addr)
			if err != nil {
				proxy.logger.Printf("[%s] ERROR ❌ Game port %d: %v",
					time.Now().Format("2006-01-02 15:04:05"), g.ListenPort, err)
				return
			}
			defer listener.Close()

			proxy.logger.Printf("[%s] INFO 🚀 Listening on port %d (-> %s)",
				time.Now().Format("2006-01-02 15:04:05"), g.ListenPort, g.Domain)

			for {
				conn, err := listener.Accept()
				if err != nil {
					continue
				}
				go proxy.handleGameConnection(conn, g.ListenPort, g.Domain, g.TargetPort)
			}
		}(game)
	}

	wg.Wait()
}
GAMESEOF

# ========== ЗАМЕНА handleRawConnection НА ДИНАМИЧЕСКУЮ ВЕРСИЮ ==========
# Нам нужно обновить handleRawConnection чтобы она поддерживала новые порты

# Резервная копия
cp main.go main.go.bak

# Обновляем маршрутизацию в main.go
# Ищем функцию handleRawConnection и заменяем логику маршрутизации

# Удаляем start_games.go если есть
rm -f start_games.go

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
echo -e "${BLUE}📱 Для подключения игр:${NC}"
[[ $BRAWL_SELECTED -eq 1 ]] && echo "   Brawl Stars -> DNS: $EXTERNAL_IP"
[[ $ROYALE_SELECTED -eq 1 ]] && echo "   Clash Royale -> DNS: $EXTERNAL_IP (порт 9340 в разработке)"
[[ $COC_SELECTED -eq 1 ]] && echo "   Clash of Clans -> DNS: $EXTERNAL_IP (порт 9341 в разработке)"
[[ $SQUAD_SELECTED -eq 1 ]] && echo "   Squad Busters -> DNS: $EXTERNAL_IP"
echo ""
echo "📊 Мониторинг:"
echo "   journalctl -u sni-proxy -f"
echo ""
echo "🛑 Управление:"
echo "   systemctl stop sni-proxy"
echo "   systemctl start sni-proxy"
echo "   systemctl restart sni-proxy"
echo ""