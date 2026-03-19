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
	AllowUnknownSNI   bool   `json:"allow_unknown_sni"`
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
		config.ConnectionTimeout = 30
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = 3600
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

// extractSNIWithBuffer извлекает SNI и возвращает ВСЕ прочитанные данные
func extractSNIWithBuffer(conn net.Conn) (string, []byte, error) {
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Читать первые 5 байт (заголовок TLS)
	header := make([]byte, 5)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", nil, fmt.Errorf("ошибка чтения заголовка TLS: %w", err)
	}

	// Проверить тип записи (0x16 = Handshake)
	if header[0] != 0x16 {
		return "", nil, fmt.Errorf("не TLS handshake: type=%d (возможно HTTP/2 или WebSocket)", header[0])
	}

	// Получить длину записи
	length := int(binary.BigEndian.Uint16(header[3:5]))
	if length > 65535 || length < 100 {
		return "", nil, fmt.Errorf("некорректная длина записи: %d байт (ClientHello должен быть 100-500 байт)", length)
	}

	// Читать ВСЁ сообщение handshake
	handshake := make([]byte, length)
	if _, err := io.ReadFull(conn, handshake); err != nil {
		return "", nil, fmt.Errorf("ошибка чтения handshake: %w", err)
	}

	// Собрать полный буфер
	fullBuf := append(header, handshake...)

	// Парсить SNI из handshake
	sni := parseSNI(handshake)

	return sni, fullBuf, nil
}

// parseSNI извлекает SNI из TLS handshake сообщения
func parseSNI(handshake []byte) string {
	if len(handshake) < 77 {
		return ""
	}

	// Пропустить: type(1) + length(3) + version(2) + random(32) + session_id_len(1)
	sessionIDLen := int(handshake[38])
	offset := 39 + sessionIDLen
	if offset+2 > len(handshake) {
		return ""
	}

	// Cipher suites
	cipherSuiteLen := int(binary.BigEndian.Uint16(handshake[offset : offset+2]))
	offset += 2 + cipherSuiteLen
	if offset+1 > len(handshake) {
		return ""
	}

	// Compression methods
	compressionLen := int(handshake[offset])
	offset += 1 + compressionLen
	if offset+2 > len(handshake) {
		return ""
	}

	// Extensions
	extensionsLen := int(binary.BigEndian.Uint16(handshake[offset : offset+2]))
	offset += 2
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
			nameLen := int(binary.BigEndian.Uint16(handshake[offset+3 : offset+5]))
			if offset+5+nameLen <= extensionsEnd {
				return string(handshake[offset+5 : offset+5+nameLen])
			}
		}

		offset += extLen
	}

	return ""
}

// findGameBySNI находит игру по SNI
func (p *SNIProxy) findGameBySNI(sni string, port int) *Game {
	sni = strings.ToLower(sni)

	if game, ok := p.domainMap[sni]; ok {
		if game.Port == port {
			return game
		}
	}

	for domain, game := range p.domainMap {
		if game.Port != port {
			continue
		}
		if strings.HasPrefix(domain, "*.") {
			suffix := domain[1:]
			if strings.HasSuffix(sni, suffix) {
				return game
			}
		}
	}

	return nil
}

// handleConnection обрабатывает подключение клиента
func (p *SNIProxy) handleConnection(clientConn net.Conn, listenPort int) {
	clientAddr := clientConn.RemoteAddr().String()
	defer clientConn.Close()

	// Извлечь SNI
	sni, headerBuf, err := extractSNIWithBuffer(clientConn)
	if err != nil {
		p.logger.Printf("[%s] WARN ⚠️ Ошибка SNI от %s: %v",
			time.Now().Format("2006-01-02 15:04:05"), clientAddr, err)
		return
	}

	if sni == "" {
		p.logger.Printf("[%s] WARN ⚠️ Пустой SNI от %s (буфер %d байт)",
			time.Now().Format("2006-01-02 15:04:05"), clientAddr, len(headerBuf))
		return
	}

	// Найти целевой домен
	game := p.findGameBySNI(sni, listenPort)
	targetDomain := sni
	targetPort := 443

	if game != nil {
		targetDomain = game.Domains[0]
		if game.TargetPort > 0 {
			targetPort = game.TargetPort
		}
	} else if p.config.AllowUnknownSNI {
		p.logger.Printf("[%s] INFO 🔄 Auto-proxy: %s от %s",
			time.Now().Format("2006-01-02 15:04:05"), sni, clientAddr)
	} else {
		p.logger.Printf("[%s] WARN ⚠️ Неизвестный SNI: %s от %s",
			time.Now().Format("2006-01-02 15:04:05"), sni, clientAddr)
		return
	}

	// DNS резолвинг
	ips, err := p.resolver.Resolve(targetDomain)
	if err != nil {
		p.logger.Printf("[%s] ERROR ❌ DNS ошибка для %s: %v",
			time.Now().Format("2006-01-02 15:04:05"), targetDomain, err)
		return
	}

	if len(ips) == 0 {
		p.logger.Printf("[%s] ERROR ❌ Нет IP для %s",
			time.Now().Format("2006-01-02 15:04:05"), targetDomain)
		return
	}

	// Подключение к серверу
	targetIP := ips[0]
	targetAddr := fmt.Sprintf("%s:%d", targetIP.String(), targetPort)

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.ConnectionTimeout)*time.Second)
	defer cancel()

	var dialer net.Dialer
	serverConn, err := dialer.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		p.logger.Printf("[%s] ERROR ❌ Подключение к %s: %v",
			time.Now().Format("2006-01-02 15:04:05"), targetAddr, err)
		return
	}
	defer serverConn.Close()

	p.logger.Printf("[%s] INFO ✅ %s -> %s (%s)",
		time.Now().Format("2006-01-02 15:04:05"), clientAddr, targetAddr, sni)

	// Отправить ClientHello
	if _, err := serverConn.Write(headerBuf); err != nil {
		p.logger.Printf("[%s] ERROR ❌ Отправка ClientHello: %v", err)
		return
	}

	// Настроить соединения
	if tcpConn, ok := clientConn.(*net.TCPConn); ok {
		tcpConn.SetReadBuffer(1024 * 1024)
		tcpConn.SetWriteBuffer(1024 * 1024)
		tcpConn.SetNoDelay(true)
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(60 * time.Second)
	}
	if tcpConn, ok := serverConn.(*net.TCPConn); ok {
		tcpConn.SetReadBuffer(1024 * 1024)
		tcpConn.SetWriteBuffer(1024 * 1024)
		tcpConn.SetNoDelay(true)
		tcpConn.SetKeepAlive(true)
		tcpConn.SetKeepAlivePeriod(60 * time.Second)
	}

	// Копировать трафик
	done := make(chan int64, 2)
	readTimeout := 300 * time.Second // 5 минут
	idleTimeout := time.Duration(p.config.IdleTimeout) * time.Second

	go func() {
		bytes := copyData(serverConn, clientConn, readTimeout, idleTimeout)
		done <- bytes
	}()

	go func() {
		bytes := copyData(clientConn, serverConn, readTimeout, idleTimeout)
		done <- bytes
	}()

	// Ждать оба направления
	bytes1 := <-done
	bytes2 := <-done

	p.logger.Printf("[%s] INFO 📊 Трафик: c→s=%d байт, s→c=%d байт",
		time.Now().Format("2006-01-02 15:04:05"), bytes1, bytes2)
}

// copyData копирует данные между соединениями
func copyData(dst net.Conn, src net.Conn, readTimeout, idleTimeout time.Duration) int64 {
	var total int64 = 0
	buf := make([]byte, 256*1024) // 256KB буфер

	for {
		src.SetReadDeadline(time.Now().Add(readTimeout))
		n, err := src.Read(buf)

		if n > 0 {
			total += int64(n)
			dst.SetWriteDeadline(time.Now().Add(idleTimeout))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total
			}
		}

		if err != nil {
			return total
		}
	}
}

// Start запускает прослушивание портов
func (p *SNIProxy) Start() error {
	for _, port := range p.config.ListenPorts {
		addr := fmt.Sprintf(":%d", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("ошибка порта %d: %w", port, err)
		}

		p.listeners = append(p.listeners, ln)
		p.logger.Printf("[%s] INFO 🚀 Listening on port %d",
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

// Shutdown корректная остановка
func (p *SNIProxy) Shutdown() {
	p.logger.Printf("[%s] INFO 🛑 Shutdown...",
		time.Now().Format("2006-01-02 15:04:05"))

	close(p.shutdown)

	for _, ln := range p.listeners {
		ln.Close()
	}

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		p.logger.Printf("[%s] INFO ✅ All connections closed",
			time.Now().Format("2006-01-02 15:04:05"))
	case <-time.After(30 * time.Second):
		p.logger.Printf("[%s] WARN ⚠️ Shutdown timeout",
			time.Now().Format("2006-01-02 15:04:05"))
	}

	if p.logFile != nil {
		p.logFile.Close()
	}
}

func main() {
	configPath := "config.json"
	if len(os.Args) > 1 && os.Args[1] != "--config" {
		configPath = os.Args[1]
	}
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--config" && i+1 < len(os.Args) {
			configPath = os.Args[i+1]
			break
		}
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("[%s] ERROR ❌ %v", time.Now().Format("2006-01-02 15:04:05"), err)
	}

	proxy, err := NewSNIProxy(config)
	if err != nil {
		log.Fatalf("[%s] ERROR ❌ %v", time.Now().Format("2006-01-02 15:04:05"), err)
	}

	if err := proxy.Start(); err != nil {
		log.Fatalf("[%s] ERROR ❌ %v", time.Now().Format("2006-01-02 15:04:05"), err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	proxy.Shutdown()
}
