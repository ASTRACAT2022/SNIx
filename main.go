package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Game представляет конфигурацию игры
type Game struct {
	Name       string   `json:"name"`
	Domains    []string `json:"domains"`
	Port       int      `json:"port"`
	TargetPort int      `json:"target_port"`
}

// Config представляет полную конфигурацию прокси
type Config struct {
	ListenPorts       []int  `json:"listen_ports"`
	Games             []Game `json:"games"`
	LogFile           string `json:"log_file"`
	DNSCacheTTL       int    `json:"dns_cache_ttl"`
	ConnectionTimeout int    `json:"connection_timeout"`
	IdleTimeout       int    `json:"idle_timeout"`
}

// DNSCacheEntry представляет запись кэша DNS
type DNSCacheEntry struct {
	IPs       []net.IP
	ExpiresAt time.Time
}

// DNSResolver управляет DNS-резолвингом с кэшированием
type DNSResolver struct {
	cache map[string]*DNSCacheEntry
	ttl   time.Duration
	mu    sync.RWMutex
}

// NewDNSResolver создаёт новый DNS-резолвер
func NewDNSResolver(ttlSeconds int) *DNSResolver {
	return &DNSResolver{
		cache: make(map[string]*DNSCacheEntry),
		ttl:   time.Duration(ttlSeconds) * time.Second,
	}
}

// Resolve возвращает IP-адреса для домена (из кэша или через DNS-запрос)
func (r *DNSResolver) Resolve(domain string) ([]net.IP, error) {
	r.mu.RLock()
	if entry, ok := r.cache[domain]; ok {
		if time.Now().Before(entry.ExpiresAt) {
			r.mu.RUnlock()
			return entry.IPs, nil
		}
	}
	r.mu.RUnlock()

	// DNS-запрос
	ips, err := net.LookupIP(domain)
	if err != nil {
		return nil, err
	}

	// Кэширование
	r.mu.Lock()
	r.cache[domain] = &DNSCacheEntry{
		IPs:       ips,
		ExpiresAt: time.Now().Add(r.ttl),
	}
	r.mu.Unlock()

	return ips, nil
}

// SNIProxy представляет основной прокси-сервер
type SNIProxy struct {
	config     *Config
	resolver   *DNSResolver
	domainMap  map[string]*Game
	listeners  []net.Listener
	wg         sync.WaitGroup
	shutdown   chan struct{}
	logger     *log.Logger
	logFile    *os.File
}

// LoadConfig загружает конфигурацию из JSON-файла
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения конфига: %w", err)
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("ошибка парсинга конфига: %w", err)
	}

	// Установить значения по умолчанию
	if config.DNSCacheTTL == 0 {
		config.DNSCacheTTL = 300
	}
	if config.ConnectionTimeout == 0 {
		config.ConnectionTimeout = 10
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = 18000
	}

	return &config, nil
}

// NewSNIProxy создаёт новый SNI-прокси
func NewSNIProxy(config *Config) (*SNIProxy, error) {
	proxy := &SNIProxy{
		config:    config,
		resolver:  NewDNSResolver(config.DNSCacheTTL),
		domainMap: make(map[string]*Game),
		listeners: make([]net.Listener, 0),
		shutdown:  make(chan struct{}),
	}

	// Построить мапу домен -> игра
	for i := range config.Games {
		game := &config.Games[i]
		for _, domain := range game.Domains {
			proxy.domainMap[strings.ToLower(domain)] = game
		}
	}

	// Настроить логирование
	var logWriter io.Writer = os.Stdout
	if config.LogFile != "" {
		// Создать директорию для логов если не существует
		if err := os.MkdirAll("logs", 0755); err != nil {
			log.Printf("Предупреждение: не удалось создать директорию logs: %v", err)
		}

		logPath := config.LogFile
		if !strings.HasPrefix(logPath, "/") && !strings.HasPrefix(logPath, "logs/") {
			logPath = "logs/" + logPath
		}

		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Printf("Предупреждение: не удалось открыть файл логов %s: %v", config.LogFile, err)
		} else {
			proxy.logFile = file
			logWriter = io.MultiWriter(os.Stdout, file)
		}
	}

	proxy.logger = log.New(logWriter, "", 0)

	return proxy, nil
}

// extractSNI извлекает SNI из TLS ClientHello
func extractSNI(conn net.Conn) (string, error) {
	// Установить таймаут чтения
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Читать первые 5 байт (заголовок TLS)
	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", fmt.Errorf("ошибка чтения заголовка TLS: %w", err)
	}

	// Проверить тип записи (Content Type = 0x16 для Handshake)
	if header[0] != 0x16 {
		return "", fmt.Errorf("не TLS handshake: type=%d", header[0])
	}

	// Проверить версию TLS
	version := binary.BigEndian.Uint16(header[1:3])
	if version < 0x0301 || version > 0x0304 {
		return "", fmt.Errorf("неподдерживаемая версия TLS: 0x%04x", version)
	}

	// Получить длину записи
	length := int(binary.BigEndian.Uint16(header[3:5]))
	if length > 65535 {
		return "", fmt.Errorf("слишком большая запись: %d", length)
	}

	// Читать всю запись Handshake
	handshake := make([]byte, length)
	if _, err := io.ReadFull(conn, handshake); err != nil {
		return "", fmt.Errorf("ошибка чтения handshake: %w", err)
	}

	// Пропустить тип handshake (1 байт) и длину (3 байта)
	if len(handshake) < 4 {
		return "", fmt.Errorf("слишком короткий handshake")
	}

	// Пропустить версию TLS и random (2 + 32 байта)
	if len(handshake) < 38 {
		return "", fmt.Errorf("слишком короткий handshake для версии/random")
	}

	// Пропустить session ID (1 байт длина + сама сессия)
	sessionIDLen := int(handshake[38])
	offset := 39 + sessionIDLen
	if offset > len(handshake) {
		return "", fmt.Errorf("некорректная длина session ID")
	}

	// Пропустить cipher suites (2 байта длина + сами шифры)
	if offset+2 > len(handshake) {
		return "", fmt.Errorf("некорректная длина cipher suites")
	}
	cipherSuiteLen := int(binary.BigEndian.Uint16(handshake[offset : offset+2]))
	offset += 2 + cipherSuiteLen
	if offset > len(handshake) {
		return "", fmt.Errorf("некорректная длина cipher suites")
	}

	// Пропустить compression methods (1 байт длина + методы)
	if offset+1 > len(handshake) {
		return "", fmt.Errorf("некорректная длина compression methods")
	}
	compressionLen := int(handshake[offset])
	offset += 1 + compressionLen
	if offset > len(handshake) {
		return "", fmt.Errorf("некорректная длина compression methods")
	}

	// Проверить наличие extensions
	if offset+2 > len(handshake) {
		return "", nil // Нет extensions
	}
	extensionsLen := int(binary.BigEndian.Uint16(handshake[offset : offset+2]))
	offset += 2

	if offset+extensionsLen > len(handshake) {
		return "", fmt.Errorf("некорректная длина extensions")
	}

	// Парсить extensions
	extensionsEnd := offset + extensionsLen
	for offset+4 <= extensionsEnd {
		extType := binary.BigEndian.Uint16(handshake[offset : offset+2])
		extLen := int(binary.BigEndian.Uint16(handshake[offset+2 : offset+4]))
		offset += 4

		if offset+extLen > extensionsEnd {
			break
		}

		// Extension type 0 = SNI
		if extType == 0 && extLen >= 5 {
			// Пропустить длину списка SNI (2 байта) и тип (1 байт)
			// Получить длину домена (2 байта)
			nameLen := int(binary.BigEndian.Uint16(handshake[offset+3 : offset+5]))
			if offset+5+nameLen <= extensionsEnd {
				sni := string(handshake[offset+5 : offset+5+nameLen])
				return sni, nil
			}
		}

		offset += extLen
	}

	return "", nil
}

// findGameBySNI находит игру по SNI
func (p *SNIProxy) findGameBySNI(sni string, port int) *Game {
	sni = strings.ToLower(sni)

	// Точное совпадение
	if game, ok := p.domainMap[sni]; ok {
		if game.Port == port {
			return game
		}
	}

	//Wildcard matching (например, *.example.com)
	for domain, game := range p.domainMap {
		if game.Port != port {
			continue
		}
		if strings.HasPrefix(domain, "*.") {
			suffix := domain[1:] // .example.com
			if strings.HasSuffix(sni, suffix) {
				return game
			}
		}
	}

	return nil
}

// handleConnection обрабатывает подключение клиента
func (p *SNIProxy) handleConnection(clientConn net.Conn, listenPort int) {
	defer clientConn.Close()

	clientAddr := clientConn.RemoteAddr().String()

	// Извлечь SNI
	sni, err := extractSNI(clientConn)
	if err != nil {
		p.logger.Printf("[%s] WARN  ⚠️ Ошибка извлечения SNI от %s: %v",
			time.Now().Format("2006-01-02 15:04:05"), clientAddr, err)
		return
	}

	if sni == "" {
		p.logger.Printf("[%s] WARN  ⚠️ Неизвестный SNI (пустой) от %s",
			time.Now().Format("2006-01-02 15:04:05"), clientAddr)
		return
	}

	// Найти игру по SNI
	game := p.findGameBySNI(sni, listenPort)
	if game == nil {
		p.logger.Printf("[%s] WARN  ⚠️ Неизвестный SNI: %s от %s",
			time.Now().Format("2006-01-02 15:04:05"), sni, clientAddr)
		return
	}

	// Резолвить DNS
	ips, err := p.resolver.Resolve(game.Domains[0])
	if err != nil {
		p.logger.Printf("[%s] ERROR ❌ Ошибка DNS для %s: %v",
			time.Now().Format("2006-01-02 15:04:05"), game.Domains[0], err)
		return
	}

	if len(ips) == 0 {
		p.logger.Printf("[%s] ERROR ❌ Нет IP-адресов для %s",
			time.Now().Format("2006-01-02 15:04:05"), game.Domains[0])
		return
	}

	// Выбрать первый IP (можно улучшить с балансировкой)
	targetIP := ips[0]
	targetPort := game.TargetPort
	targetAddr := fmt.Sprintf("%s:%d", targetIP.String(), targetPort)

	// Подключиться к целевому серверу
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.ConnectionTimeout)*time.Second)
	defer cancel()

	var dialer net.Dialer
	serverConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		p.logger.Printf("[%s] ERROR ❌ Ошибка подключения к %s: %v",
			time.Now().Format("2006-01-02 15:04:05"), targetAddr, err)
		return
	}
	defer serverConn.Close()

	p.logger.Printf("[%s] INFO  ✅ %s -> %s (%s)",
		time.Now().Format("2006-01-02 15:04:05"), clientAddr, targetAddr, sni)

	// Копировать трафик в обе стороны
	done := make(chan struct{}, 2)
	idleTimeout := time.Duration(p.config.IdleTimeout) * time.Second

	go func() {
		copyWithTimeout(serverConn, clientConn, idleTimeout)
		done <- struct{}{}
	}()

	go func() {
		// Отправить сохранённый ClientHello серверу
		copyWithTimeout(clientConn, serverConn, idleTimeout)
		done <- struct{}{}
	}()

	// Ждать завершения хотя бы одного направления
	<-done
}

// copyWithTimeout копирует данные с таймаутом бездействия
func copyWithTimeout(dst net.Conn, src net.Conn, timeout time.Duration) {
	for {
		src.SetReadDeadline(time.Now().Add(timeout))
		
		buf := make([]byte, 32*1024)
		n, err := src.Read(buf)
		
		if n > 0 {
			dst.SetWriteDeadline(time.Now().Add(timeout))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		
		if err != nil {
			return
		}
	}
}

// Start запускает прослушивание портов
func (p *SNIProxy) Start() error {
	for _, port := range p.config.ListenPorts {
		addr := fmt.Sprintf(":%d", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("ошибка прослушивания порта %d: %w", port, err)
		}

		p.listeners = append(p.listeners, ln)
		p.logger.Printf("[%s] INFO  🚀 Listening on port %d",
			time.Now().Format("2006-01-02 15:04:05"), port)

		p.wg.Add(1)
		go func(listener net.Listener, port int) {
			defer p.wg.Done()

			for {
				select {
				case <-p.shutdown:
					listener.Close()
					return
				default:
					conn, err := listener.Accept()
					if err != nil {
						select {
						case <-p.shutdown:
							return
						default:
							p.logger.Printf("[%s] ERROR ❌ Ошибка Accept на порту %d: %v",
								time.Now().Format("2006-01-02 15:04:05"), port, err)
							continue
						}
					}

					p.wg.Add(1)
					go func(c net.Conn) {
						defer p.wg.Done()
						p.handleConnection(c, port)
					}(conn)
				}
			}
		}(ln, port)
	}

	return nil
}

// Shutdown выполняет корректную остановку
func (p *SNIProxy) Shutdown() {
	p.logger.Printf("[%s] INFO  🛑 Graceful shutdown initiated...",
		time.Now().Format("2006-01-02 15:04:05"))

	close(p.shutdown)

	// Закрыть все слушатели
	for _, ln := range p.listeners {
		ln.Close()
	}

	// Ждать завершения всех подключений (с таймаутом)
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.logger.Printf("[%s] INFO  ✅ Все подключения закрыты",
			time.Now().Format("2006-01-02 15:04:05"))
	case <-time.After(30 * time.Second):
		p.logger.Printf("[%s] WARN  ⚠️ Таймаут ожидания завершения подключений",
			time.Now().Format("2006-01-02 15:04:05"))
	}

	if p.logFile != nil {
		p.logFile.Close()
	}
}

func main() {
	// Загрузить конфигурацию
	configPath := "config.json"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("[%s] ERROR ❌ %v", time.Now().Format("2006-01-02 15:04:05"), err)
	}

	// Создать прокси
	proxy, err := NewSNIProxy(config)
	if err != nil {
		log.Fatalf("[%s] ERROR ❌ %v", time.Now().Format("2006-01-02 15:04:05"), err)
	}

	// Запустить
	if err := proxy.Start(); err != nil {
		log.Fatalf("[%s] ERROR ❌ %v", time.Now().Format("2006-01-02 15:04:05"), err)
	}

	// Обработка сигналов
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	proxy.Shutdown()
}
