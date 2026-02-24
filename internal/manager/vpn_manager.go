package manager

import (
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"

	"outline-cli-ws/internal/config"
	"outline-cli-ws/internal/shadowsocks"
	"outline-cli-ws/internal/transport"
)

type ConnectionStatus struct {
	State     string
	Server    *config.ServerConfig
	Upload    int64
	Download  int64
	StartTime time.Time
}

type VPNManager struct {
	config       *config.GlobalConfig
	mu           sync.RWMutex
	status       *ConnectionStatus
	cmd          *exec.Cmd
	stopChan     chan struct{}
	uploadChan   chan int64
	downloadChan chan int64
}

func NewVPNManager(cfg *config.GlobalConfig) *VPNManager {
	return &VPNManager{
		config:   cfg,
		status:   &ConnectionStatus{State: "disconnected"},
		stopChan: make(chan struct{}),
	}
}

func (m *VPNManager) Connect(server *config.ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == "connected" {
		return fmt.Errorf("already connected to %s", m.status.Server.Name)
	}

	// Создаем dialer для транспорта
	dialer, err := transport.CreateDialer(server)
	if err != nil {
		return fmt.Errorf("failed to create dialer: %w", err)
	}

	// Создаем cipher
	cipher, err := shadowsocks.NewCipher(server.Method, server.Password)
	if err != nil {
		return fmt.Errorf("failed to create cipher: %w", err)
	}

	// Запускаем локальный SOCKS5 сервер
	localAddr := fmt.Sprintf("%s:%d", m.config.LocalAddr, m.config.LocalPort)
	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("failed to start local server: %w", err)
	}

	m.status = &ConnectionStatus{
		State:     "connecting",
		Server:    server,
		StartTime: time.Now(),
	}

	go m.handleConnections(listener, dialer, cipher)
	go m.monitorConnection()

	m.status.State = "connected"
	return nil
}

func (m *VPNManager) handleConnections(listener net.Listener, dialer transport.Dialer, cipher shadowsocks.Cipher) {
	defer listener.Close()

	for {
		select {
		case <-m.stopChan:
			return
		default:
			localConn, err := listener.Accept()
			if err != nil {
				continue
			}

			go m.handleConnection(localConn, dialer, cipher)
		}
	}
}

func (m *VPNManager) handleConnection(localConn net.Conn, dialer transport.Dialer, cipher shadowsocks.Cipher) {
	defer localConn.Close()

	// Устанавливаем соединение с сервером
	remoteConn, err := dialer.DialContext(nil)
	if err != nil {
		return
	}
	defer remoteConn.Close()

	// Создаем shadowsocks соединение
	ssConn := shadowsocks.NewConn(remoteConn, cipher, true)

	// Читаем SOCKS5 запрос
	buf := make([]byte, 256)
	n, err := localConn.Read(buf)
	if err != nil {
		return
	}

	// Проверяем SOCKS5 версию
	if buf[0] != 0x05 {
		return
	}

	// Отправляем ответ
	localConn.Write([]byte{0x05, 0x00})

	// Читаем целевой адрес
	n, err = localConn.Read(buf)
	if err != nil {
		return
	}

	if buf[1] != 0x01 { // Только CONNECT команда
		return
	}

	// Получаем целевой адрес
	addr := buf[3:n]

	// Отправляем адрес на сервер
	if _, err := ssConn.Write(addr); err != nil {
		return
	}

	// Отправляем SOCKS5 ответ
	localConn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})

	// Проксируем данные
	m.proxyConnections(localConn, ssConn)
}

func (m *VPNManager) proxyConnections(local, remote net.Conn) {
	var upload, download int64
	done := make(chan struct{})

	// local -> remote (upload)
	go func() {
		n, _ := io.Copy(remote, local)
		upload = n
		done <- struct{}{}
	}()

	// remote -> local (download)
	go func() {
		n, _ := io.Copy(local, remote)
		download = n
		done <- struct{}{}
	}()

	<-done
	<-done

	m.mu.Lock()
	m.status.Upload += upload
	m.status.Download += download
	m.mu.Unlock()
}

func (m *VPNManager) monitorConnection() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			// Проверяем соединение
			conn, err := net.DialTimeout("tcp",
				fmt.Sprintf("%s:%d", m.config.LocalAddr, m.config.LocalPort),
				5*time.Second)
			if err != nil {
				m.mu.Lock()
				m.status.State = "disconnected"
				m.mu.Unlock()
				return
			}
			conn.Close()
		}
	}
}

func (m *VPNManager) Disconnect() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status.State == "disconnected" {
		return nil
	}

	close(m.stopChan)
	m.stopChan = make(chan struct{})

	m.status.State = "disconnected"
	m.status.Server = nil

	return nil
}

func (m *VPNManager) GetStatus() *ConnectionStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Возвращаем копию
	status := *m.status
	return &status
}
