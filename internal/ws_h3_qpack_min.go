package internal

import (
	"bytes"
	"errors"
	"fmt"

	"golang.org/x/net/http2/hpack"
)

type h3tableEntry struct {
	name  string
	value string
}

var errH3QPACK = errors.New("h3 qpack decode failed")

var h3StaticTable = [...]h3tableEntry{
	0: {":authority", ""}, 1: {":path", "/"}, 2: {"age", "0"}, 3: {"content-disposition", ""}, 4: {"content-length", "0"}, 5: {"cookie", ""}, 6: {"date", ""}, 7: {"etag", ""}, 8: {"if-modified-since", ""}, 9: {"if-none-match", ""}, 10: {"last-modified", ""}, 11: {"link", ""}, 12: {"location", ""}, 13: {"referer", ""}, 14: {"set-cookie", ""}, 15: {":method", "CONNECT"}, 16: {":method", "DELETE"}, 17: {":method", "GET"}, 18: {":method", "HEAD"}, 19: {":method", "OPTIONS"}, 20: {":method", "POST"}, 21: {":method", "PUT"}, 22: {":scheme", "http"}, 23: {":scheme", "https"}, 24: {":status", "103"}, 25: {":status", "200"}, 26: {":status", "304"}, 27: {":status", "404"}, 28: {":status", "503"}, 29: {"accept", "*/*"}, 30: {"accept", "application/dns-message"}, 31: {"accept-encoding", "gzip, deflate, br"}, 32: {"accept-ranges", "bytes"}, 33: {"access-control-allow-headers", "cache-control"}, 34: {"access-control-allow-headers", "content-type"}, 35: {"access-control-allow-origin", "*"}, 36: {"cache-control", "max-age=0"}, 37: {"cache-control", "max-age=2592000"}, 38: {"cache-control", "max-age=604800"}, 39: {"cache-control", "no-cache"}, 40: {"cache-control", "no-store"}, 41: {"cache-control", "public, max-age=31536000"}, 42: {"content-encoding", "br"}, 43: {"content-encoding", "gzip"}, 44: {"content-type", "application/dns-message"}, 45: {"content-type", "application/javascript"}, 46: {"content-type", "application/json"}, 47: {"content-type", "application/x-www-form-urlencoded"}, 48: {"content-type", "image/gif"}, 49: {"content-type", "image/jpeg"}, 50: {"content-type", "image/png"}, 51: {"content-type", "text/css"}, 52: {"content-type", "text/html; charset=utf-8"}, 53: {"content-type", "text/plain"}, 54: {"content-type", "text/plain;charset=utf-8"}, 55: {"range", "bytes=0-"}, 56: {"strict-transport-security", "max-age=31536000"}, 57: {"strict-transport-security", "max-age=31536000; includesubdomains"}, 58: {"strict-transport-security", "max-age=31536000; includesubdomains; preload"}, 59: {"vary", "accept-encoding"}, 60: {"vary", "origin"}, 61: {"x-content-type-options", "nosniff"}, 62: {"x-xss-protection", "1; mode=block"}, 63: {":status", "100"}, 64: {":status", "204"}, 65: {":status", "206"}, 66: {":status", "302"}, 67: {":status", "400"}, 68: {":status", "403"}, 69: {":status", "421"}, 70: {":status", "425"}, 71: {":status", "500"}, 72: {"accept-language", ""}, 73: {"access-control-allow-credentials", "FALSE"}, 74: {"access-control-allow-credentials", "TRUE"}, 75: {"access-control-allow-headers", "*"}, 76: {"access-control-allow-methods", "get"}, 77: {"access-control-allow-methods", "get, post, options"}, 78: {"access-control-allow-methods", "options"}, 79: {"access-control-expose-headers", "content-length"}, 80: {"access-control-request-headers", "content-type"}, 81: {"access-control-request-method", "get"}, 82: {"access-control-request-method", "post"}, 83: {"alt-svc", "clear"}, 84: {"authorization", ""}, 85: {"content-security-policy", "script-src 'none'; object-src 'none'; base-uri 'none'"}, 86: {"early-data", "1"}, 87: {"expect-ct", ""}, 88: {"forwarded", ""}, 89: {"if-range", ""}, 90: {"origin", ""}, 91: {"purpose", "prefetch"}, 92: {"server", ""}, 93: {"timing-allow-origin", "*"}, 94: {"upgrade-insecure-requests", "1"}, 95: {"user-agent", ""}, 96: {"x-forwarded-for", ""}, 97: {"x-frame-options", "deny"}, 98: {"x-frame-options", "sameorigin"},
}

func h3EncodeHeaders(headers [][2]string) []byte {
	b := []byte{0x00, 0x00} // required insert count + delta base
	for _, kv := range headers {
		name, value := kv[0], kv[1]
		idxName, idxNameVal := -1, -1
		for i, e := range h3StaticTable {
			if idxName < 0 && e.name == name {
				idxName = i
			}
			if e.name == name && e.value == value {
				idxNameVal = i
				break
			}
		}
		if idxNameVal >= 0 {
			b = appendPrefixedInt(b, 0b1000_0000|0b0100_0000, 6, int64(idxNameVal))
			continue
		}
		if idxName >= 0 {
			b = appendPrefixedInt(b, 0b0100_0000|0b0001_0000, 4, int64(idxName))
			b = appendPrefixedString(b, 0, 7, value)
			continue
		}
		b = appendPrefixedString(b, 0b0010_0000, 3, name)
		b = appendPrefixedString(b, 0, 7, value)
	}
	return b
}

func h3DecodeHeaders(block []byte) (map[string]string, error) {
	r := bytes.NewReader(block)
	if _, err := readPrefixedInt(r, 8); err != nil {
		return nil, err
	}
	if _, err := readPrefixedInt(r, 7); err != nil {
		return nil, err
	}
	h := map[string]string{}
	for r.Len() > 0 {
		b, err := r.ReadByte()
		if err != nil {
			return nil, errH3QPACK
		}
		switch {
		case b&0b1000_0000 != 0:
			idx, err := readPrefixedIntWithFirst(r, b, 6)
			if err != nil {
				return nil, err
			}
			if idx >= int64(len(h3StaticTable)) {
				return nil, errH3QPACK
			}
			e := h3StaticTable[idx]
			h[e.name] = e.value
		case b&0b1110_0000 == 0b0100_0000:
			idx, err := readPrefixedIntWithFirst(r, b, 4)
			if err != nil {
				return nil, err
			}
			if idx >= int64(len(h3StaticTable)) {
				return nil, errH3QPACK
			}
			name := h3StaticTable[idx].name
			_, val, err := readPrefixedString(r, 7)
			if err != nil {
				return nil, err
			}
			h[name] = val
		case b&0b1110_0000 == 0b0010_0000:
			_, name, err := readPrefixedStringWithFirst(r, b, 3)
			if err != nil {
				return nil, err
			}
			_, val, err := readPrefixedString(r, 7)
			if err != nil {
				return nil, err
			}
			h[name] = val
		default:
			return nil, fmt.Errorf("%w: unsupported qpack line 0x%x", errH3QPACK, b)
		}
	}
	return h, nil
}

func appendPrefixedInt(b []byte, first byte, prefix uint8, v int64) []byte {
	mask := int64((1 << prefix) - 1)
	if v < mask {
		return append(b, first|byte(v))
	}
	b = append(b, first|byte(mask))
	v -= mask
	for v >= 128 {
		b = append(b, byte(v%128+128))
		v /= 128
	}
	return append(b, byte(v))
}

func readPrefixedInt(r *bytes.Reader, prefix uint8) (int64, error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, errH3QPACK
	}
	return readPrefixedIntWithFirst(r, b, prefix)
}

func readPrefixedIntWithFirst(r *bytes.Reader, b byte, prefix uint8) (int64, error) {
	mask := byte((1 << prefix) - 1)
	v := int64(b & mask)
	if v != int64(mask) {
		return v, nil
	}
	m := 0
	for {
		x, err := r.ReadByte()
		if err != nil {
			return 0, errH3QPACK
		}
		v += int64(x&127) << m
		if x&128 == 0 {
			return v, nil
		}
		m += 7
	}
}

func appendPrefixedString(b []byte, first byte, prefix uint8, s string) []byte {
	b = appendPrefixedInt(b, first, prefix, int64(len(s)))
	return append(b, s...)
}

func readPrefixedString(r *bytes.Reader, prefix uint8) (bool, string, error) {
	b, err := r.ReadByte()
	if err != nil {
		return false, "", errH3QPACK
	}
	return readPrefixedStringWithFirst(r, b, prefix)
}

func readPrefixedStringWithFirst(r *bytes.Reader, b byte, prefix uint8) (bool, string, error) {
	huffman := b&(1<<prefix) != 0
	n, err := readPrefixedIntWithFirst(r, b, prefix)
	if err != nil {
		return false, "", err
	}
	s := make([]byte, n)
	if _, err := r.Read(s); err != nil {
		return false, "", errH3QPACK
	}
	if !huffman {
		return false, string(s), nil
	}
	dec, err := hpack.HuffmanDecodeToString(s)
	if err != nil {
		return false, "", errH3QPACK
	}
	return true, dec, nil
}
