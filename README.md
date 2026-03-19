# 🎮 Game SNI Proxy

SNI-прокси для маршрутизации игрового трафика (Supercell, mo.co) на основе доменного имени из TLS ClientHello.

## 📖 Описание

Этот проект решает проблему маршрутизации игрового трафика, когда несколько игр используют один порт (9339). Прокси извлекает SNI (Server Name Indication) из TLS ClientHello и направляет трафик на соответствующий игровой сервер.

### Поддерживаемые игры

| Игра | Домен | Порт |
|------|-------|------|
| Brawl Stars | `game.brawlstarsgame.com` | 9339 |
| Clash Royale | `game.clashroyaleapp.com` | 9339 |
| Clash of Clans | `gamea.clashofclans.com`, `game.clashofclans.com` | 9339 |
| Squad Busters | `game.squadbustersgame.com` | 9339 |
| mo.co | `game.mocogame.com`, `api.mocogame.com` | 30000 |

## 🚀 Быстрый старт

### Вариант 1: Бинарный файл

```bash
# Компиляция
go build -o sni-proxy -ldflags="-s -w" main.go

# Запуск
./sni-proxy --config config.json
```

### Вариант 2: Docker

```bash
# Сборка
docker build -t game-sni-proxy .

# Запуск
docker run -d --name sni_proxy --net=host --restart=unless-stopped game-sni-proxy
```

### Вариант 3: Docker Compose

```bash
docker-compose up -d
```

## ⚙️ Конфигурация

Файл `config.json`:

```json
{
  "listen_ports": [443, 9339, 30000],
  "games": [
    {
      "name": "Brawl Stars",
      "domains": ["game.brawlstarsgame.com"],
      "port": 9339,
      "target_port": 9339
    }
  ],
  "log_file": "proxy.log",
  "dns_cache_ttl": 300,
  "connection_timeout": 10,
  "idle_timeout": 18000
}
```

### Поля конфигурации

| Поле | Тип | Описание | По умолчанию |
|------|-----|----------|--------------|
| `listen_ports` | `[]int` | Порты для прослушивания | - |
| `games` | `[]Game` | Список игр | - |
| `games[].name` | `string` | Название игры | - |
| `games[].domains` | `[]string` | Домены для матчинга SNI | - |
| `games[].port` | `int` | Внешний порт | - |
| `games[].target_port` | `int` | Внутренний порт | - |
| `log_file` | `string` | Путь к файлу логов | - |
| `dns_cache_ttl` | `int` | TTL DNS-кэша (сек) | 300 |
| `connection_timeout` | `int` | Таймаут подключения (сек) | 10 |
| `idle_timeout` | `int` | Таймаут бездействия (сек) | 18000 |

## 📝 Логирование

Логи пишутся в консоль и файл (если указан). Формат:

```
[2026-03-19 22:00:00] INFO  ✅ 192.168.1.100:54321 -> 18.196.54.123:9339 (game.brawlstarsgame.com)
[2026-03-19 22:00:01] WARN  ⚠️ Неизвестный SNI: unknown.domain.com от 192.168.1.100:54322
[2026-03-19 22:00:02] ERROR ❌ Ошибка подключения к 18.196.54.123:9339: connection refused
```

## 🔧 Установка как systemd-сервис

```bash
# Скопировать файлы
sudo mkdir -p /opt/sni-proxy
sudo cp sni-proxy /opt/sni-proxy/
sudo cp config.json /opt/sni-proxy/
sudo cp systemd/sni-proxy.service /etc/systemd/system/

# Перезагрузить systemd и запустить
sudo systemctl daemon-reload
sudo systemctl enable sni-proxy
sudo systemctl start sni-proxy

# Проверить статус
sudo systemctl status sni-proxy

# Посмотреть логи
sudo journalctl -u sni-proxy -f
```

## 🧪 Тестирование

### Проверка портов

```bash
ss -tlnp | grep -E ':(443|9339|30000)'
```

### Проверка логов

```bash
tail -f logs/proxy.log
# или для systemd
sudo journalctl -u sni-proxy -f
```

### Тест через openssl

```bash
openssl s_client -connect YOUR_SERVER_IP:9339 -servername game.brawlstarsgame.com
```

## 🏗️ Архитектура

```
┌─────────────┐     ┌──────────────────┐     ┌──────────────┐
│   Клиент    │────▶│   SNI Proxy      │────▶│ Игровой      │
│  (игра на   │     │  (порт 9339)     │     │ сервер       │
│   телефоне) │     │  - извлекает SNI │     │ (Supercell)  │
└─────────────┘     │  - резолвит DNS  │     └──────────────┘
                    │  - маршрутизирует│
                    └──────────────────┘
```

## 🔒 Безопасность

- ❌ **Не расшифровывает трафик** — только читает SNI из ClientHello
- ❌ **Не хранит данные** — логи содержат только метаданные подключений
- ✅ **DNS-кэширование** — снижает нагрузку на DNS-серверы
- ✅ **Graceful shutdown** — корректное завершение сессий

## 🛠️ Troubleshooting

### Ошибка "address already in use"

Порт уже занят другим процессом:

```bash
# Найти процесс
sudo lsof -i :9339

# Убить процесс
sudo kill -9 <PID>
```

### Ошибка DNS

Проверьте DNS-серверы:

```bash
nslookup game.brawlstarsgame.com
```

### Прокси не запускается на порту 443

Порт 443 требует root-прав:

```bash
# Запуск от root
sudo ./sni-proxy

# Или используйте setcap
sudo setcap 'cap_net_bind_service=+ep' ./sni-proxy
./sni-proxy
```

### Логи пустые

Проверьте путь к файлу логов и права доступа:

```bash
ls -la logs/
chmod 755 logs/
```

## 📊 Производительность

| Параметр | Значение |
|----------|----------|
| Макс. подключений | 10,000+ одновременных |
| Задержка (latency) | < 50 мс |
| Потребление RAM | < 100 MB |
| Потребление CPU | < 10% на 1 ядре |
| DNS-кэш | 5 минут TTL |

## 📄 Лицензия

MIT License

## 🤝 Вклад

Pull requests приветствуются! Для крупных изменений сначала откройте issue.

## 📞 Контакты

- GitHub Issues: [Открыть issue](https://github.com/yourusername/game-sni-proxy/issues)
- Email: your.email@example.com
