package shadowsocks

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"
)

type Cipher interface {
	Encrypt(dst, src []byte) (int, error)
	Decrypt(dst, src []byte) (int, error)
	KeySize() int
	SaltSize() int
	NonceSize() int
}

type AEADCipher struct {
	cipher cipher.AEAD
}

func (c *AEADCipher) Encrypt(dst, src []byte) (int, error) {
	if len(dst) < len(src)+c.cipher.Overhead() {
		return 0, fmt.Errorf("destination buffer too small")
	}

	nonce := make([]byte, c.cipher.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return 0, err
	}

	// Копируем nonce в начало dst
	copy(dst, nonce)

	// Шифруем данные
	ciphertext := c.cipher.Seal(dst[:len(nonce)], nonce, src, nil)
	return len(ciphertext), nil
}

func (c *AEADCipher) Decrypt(dst, src []byte) (int, error) {
	if len(src) < c.cipher.NonceSize() {
		return 0, fmt.Errorf("ciphertext too short")
	}

	nonce := src[:c.cipher.NonceSize()]
	ciphertext := src[c.cipher.NonceSize():]

	plaintext, err := c.cipher.Open(dst[:0], nonce, ciphertext, nil)
	if err != nil {
		return 0, err
	}

	return len(plaintext), nil
}

func (c *AEADCipher) KeySize() int {
	return 32 // Для AEAD шифров
}

func (c *AEADCipher) SaltSize() int {
	return 32
}

func (c *AEADCipher) NonceSize() int {
	return c.cipher.NonceSize()
}

type StreamCipher struct {
	encryptStream cipher.Stream
	decryptStream cipher.Stream
}

func (c *StreamCipher) Encrypt(dst, src []byte) (int, error) {
	c.encryptStream.XORKeyStream(dst, src)
	return len(src), nil
}

func (c *StreamCipher) Decrypt(dst, src []byte) (int, error) {
	c.decryptStream.XORKeyStream(dst, src)
	return len(src), nil
}

func (c *StreamCipher) KeySize() int {
	return 32
}

func (c *StreamCipher) SaltSize() int {
	return 0
}

func (c *StreamCipher) NonceSize() int {
	return 0
}

func NewCipher(method, password string) (Cipher, error) {
	method = strings.ToLower(method)

	switch method {
	case "aes-256-gcm":
		return newAES256GCM(password)
	case "chacha20-ietf-poly1305":
		return newChaCha20Poly1305(password)
	case "aes-128-gcm":
		return newAES128GCM(password)
	case "aes-256-cfb":
		return newAES256CFB(password)
	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
}

func newAES256GCM(password string) (Cipher, error) {
	key := evpBytesToKey(32, password)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &AEADCipher{cipher: aead}, nil
}

func newAES128GCM(password string) (Cipher, error) {
	key := evpBytesToKey(16, password)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	return &AEADCipher{cipher: aead}, nil
}

func newChaCha20Poly1305(password string) (Cipher, error) {
	key := evpBytesToKey(32, password)
	aead, err := chacha20poly1305.New(key)
	if err != nil {
		return nil, err
	}

	return &AEADCipher{cipher: aead}, nil
}

func newAES256CFB(password string) (Cipher, error) {
	key := evpBytesToKey(32, password)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// Для CFB нужен IV, который будет отправлен первым
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}

	return &StreamCipher{
		encryptStream: cipher.NewCFBEncrypter(block, iv),
		decryptStream: cipher.NewCFBDecrypter(block, iv),
	}, nil
}

func evpBytesToKey(keySize int, password string) []byte {
	var digest []byte
	var prev []byte

	for len(digest) < keySize {
		h := sha1.New()
		h.Write(prev)
		h.Write([]byte(password))
		prev = h.Sum(nil)
		digest = append(digest, prev...)
	}

	return digest[:keySize]
}
