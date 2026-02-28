package internal

import "context"

// WSMessageType matches RFC 6455 opcodes we care about.
type WSMessageType uint8

const (
	// Continuation frame (RFC 6455 opcode 0). Used when a message is fragmented.
	WSMessageContinuation WSMessageType = 0

	WSMessageText   WSMessageType = 1
	WSMessageBinary WSMessageType = 2

	WSMessageClose WSMessageType = 8
	WSMessagePing  WSMessageType = 9
	WSMessagePong  WSMessageType = 10
)

type WSStatusCode uint16

const (
	WSStatusNormalClosure WSStatusCode = 1000
)

// WSConn is the minimal subset this project needs from a WebSocket connection.
type WSConn interface {
	Read(ctx context.Context) (WSMessageType, []byte, error)
	Write(ctx context.Context, typ WSMessageType, data []byte) error
	Close(code WSStatusCode, reason string) error
}
