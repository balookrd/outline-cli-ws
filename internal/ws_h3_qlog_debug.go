//go:build !unit

package internal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"golang.org/x/net/quic"
)

const h3QlogFirstPackets = 20

type h3QlogDebugHandler struct {
	mu   sync.Mutex
	sent int
	recv int
}

func (h *h3QlogDebugHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= quic.QLogLevelFrame
}

func (h *h3QlogDebugHandler) Handle(_ context.Context, r slog.Record) error {
	msg := r.Message
	attrs := collectSlogAttrs(r)

	switch msg {
	case "transport:parameters_set":
		wsDebugf("h3[qlog]: transport parameters %s", formatAttrMap(attrs))
	case "transport:packet_sent":
		h.mu.Lock()
		h.sent++
		n := h.sent
		h.mu.Unlock()
		if n <= h3QlogFirstPackets {
			wsDebugf("h3[qlog]: packet_sent #%d header=%s frames=%s", n, packetHeaderSummary(attrs), packetFrameTypes(attrs))
		}
	case "transport:packet_received":
		h.mu.Lock()
		h.recv++
		n := h.recv
		h.mu.Unlock()
		if n <= h3QlogFirstPackets {
			wsDebugf("h3[qlog]: packet_recv #%d header=%s frames=%s", n, packetHeaderSummary(attrs), packetFrameTypes(attrs))
		}
	case "connectivity:packet_dropped":
		wsDebugf("h3[qlog]: packet_dropped %s", formatAttrMap(attrs))
	}
	return nil
}

func (h *h3QlogDebugHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *h3QlogDebugHandler) WithGroup(_ string) slog.Handler      { return h }

func collectSlogAttrs(r slog.Record) map[string]string {
	out := map[string]string{}
	r.Attrs(func(a slog.Attr) bool {
		flattenAttr(out, "", a)
		return true
	})
	return out
}

func flattenAttr(out map[string]string, prefix string, a slog.Attr) {
	k := a.Key
	if prefix != "" {
		k = prefix + "." + k
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		for _, ga := range v.Group() {
			flattenAttr(out, k, ga)
		}
	case slog.KindAny:
		if frames, ok := v.Any().([]slog.Value); ok {
			for i, fv := range frames {
				gv := fv.Resolve()
				if gv.Kind() == slog.KindGroup {
					for _, fa := range gv.Group() {
						flattenAttr(out, fmt.Sprintf("%s[%d]", k, i), fa)
					}
				}
			}
			return
		}
		out[k] = fmt.Sprint(v.Any())
	default:
		out[k] = v.String()
	}
}

func packetHeaderSummary(attrs map[string]string) string {
	return fmt.Sprintf("type=%s pn=%s", attrs["header.packet_type"], attrs["header.packet_number"])
}

func packetFrameTypes(attrs map[string]string) string {
	types := make([]string, 0, 4)
	for i := 0; i < 12; i++ {
		if t := attrs[fmt.Sprintf("frames[%d].frame_type", i)]; t != "" {
			types = append(types, t)
		}
	}
	if len(types) == 0 {
		return "[]"
	}
	return fmt.Sprintf("%v", types)
}

func formatAttrMap(attrs map[string]string) string {
	if len(attrs) == 0 {
		return "{}"
	}
	return fmt.Sprintf("%v", attrs)
}
