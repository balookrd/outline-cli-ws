package shadowsocks

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

type Conn struct {
	net.Conn
	cipher   Cipher
	salt     []byte
	isClient bool
}

func NewConn(conn net.Conn, cipher Cipher, isClient bool) *Conn {
	return &Conn{
		Conn:     conn,
		cipher:   cipher,
		isClient: isClient,
	}
}

func (c *Conn) Write(b []byte) (n int, err error) {
	// Выделяем буфер для зашифрованных данных
	encrypted := make([]byte, len(b)+c.cipher.SaltSize()+c.cipher.NonceSize()+16)

	if c.isClient {
		// Для клиента: отправляем salt только один раз при первом соединении
		if c.salt == nil && c.cipher.SaltSize() > 0 {
			salt := make([]byte, c.cipher.SaltSize())
			if _, err := io.ReadFull(rand.Reader, salt); err != nil {
				return 0, err
			}
			c.salt = salt

			// Отправляем salt
			if _, err := c.Conn.Write(salt); err != nil {
				return 0, err
			}
		}

		// Шифруем данные
		encryptedLen, err := c.cipher.Encrypt(encrypted, b)
		if err != nil {
			return 0, err
		}

		return c.Conn.Write(encrypted[:encryptedLen])
	} else {
		// Для сервера: просто шифруем
		encryptedLen, err := c.cipher.Encrypt(encrypted, b)
		if err != nil {
			return 0, err
		}
		return c.Conn.Write(encrypted[:encryptedLen])
	}
}

func (c *Conn) Read(b []byte) (n int, err error) {
	if c.isClient && c.cipher.SaltSize() > 0 && c.salt == nil {
		// Читаем salt от сервера
		salt := make([]byte, c.cipher.SaltSize())
		if _, err := io.ReadFull(c.Conn, salt); err != nil {
			return 0, err
		}
		c.salt = salt
	}

	// Читаем зашифрованные данные
	encrypted := make([]byte, 4096)
	n, err = c.Conn.Read(encrypted)
	if err != nil {
		return 0, err
	}

	// Расшифровываем
	return c.cipher.Decrypt(b, encrypted[:n])
}

// SOCKS5 адрес для прокси
func ParseAddr(addr string) ([]byte, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Доменное имя
		if len(host) > 255 {
			return nil, fmt.Errorf("domain name too long")
		}

		buf := make([]byte, 0, 1+1+len(host)+2)
		buf = append(buf, 0x03) // Domain name type
		buf = append(buf, byte(len(host)))
		buf = append(buf, []byte(host)...)

		p, _ := net.LookupPort("tcp", port)
		buf = binary.BigEndian.AppendUint16(buf, uint16(p))
		return buf, nil
	} else if ip4 := ip.To4(); ip4 != nil {
		// IPv4
		buf := make([]byte, 0, 1+4+2)
		buf = append(buf, 0x01) // IPv4 type
		buf = append(buf, ip4...)

		p, _ := net.LookupPort("tcp", port)
		buf = binary.BigEndian.AppendUint16(buf, uint16(p))
		return buf, nil
	} else {
		// IPv6
		buf := make([]byte, 0, 1+16+2)
		buf = append(buf, 0x04) // IPv6 type
		buf = append(buf, ip...)

		p, _ := net.LookupPort("tcp", port)
		buf = binary.BigEndian.AppendUint16(buf, uint16(p))
		return buf, nil
	}
}
