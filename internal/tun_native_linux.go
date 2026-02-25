//go:build linux

package internal

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"github.com/songgao/water"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// ----- UDP flow table (per-flow Outline UDP session) -----

type udpFlowKey struct {
	netProto uint8 // 4 or 6
	srcIP    string
	srcPort  uint16
	dstIP    string
	dstPort  uint16
}

type udpFlow struct {
	key       udpFlowKey
	dst       string
	up        *UpstreamState
	sess      *OutlineUDPSession
	lastSeen  time.Time
	closeOnce sync.Once
}

type udpFlowTable struct {
	mu    sync.Mutex
	lb    *LoadBalancer
	cfg   TunConfig
	flows map[udpFlowKey]*udpFlow
}

func newUDPFlowTable(lb *LoadBalancer, cfg TunConfig) *udpFlowTable {
	return &udpFlowTable{
		lb:    lb,
		cfg:   cfg,
		flows: make(map[udpFlowKey]*udpFlow),
	}
}

func (t *udpFlowTable) get(key udpFlowKey) *udpFlow {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.flows[key]
}

func (t *udpFlowTable) put(key udpFlowKey, f *udpFlow) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	limit := t.cfg.UDPMaxFlows
	if limit <= 0 {
		limit = 4096
	}
	if len(t.flows) >= limit {
		return fmt.Errorf("udp flow limit reached: %d", limit)
	}
	t.flows[key] = f
	return nil
}

func (t *udpFlowTable) touch(key udpFlowKey) {
	t.mu.Lock()
	if f := t.flows[key]; f != nil {
		f.lastSeen = time.Now()
	}
	t.mu.Unlock()
}

func (t *udpFlowTable) remove(key udpFlowKey) *udpFlow {
	t.mu.Lock()
	f := t.flows[key]
	delete(t.flows, key)
	t.mu.Unlock()
	return f
}

func (t *udpFlowTable) gcOnce() {
	now := time.Now()
	idle := t.cfg.UDPIdleTimeout
	if idle <= 0 {
		idle = 60 * time.Second
	}

	var toClose []*udpFlow

	t.mu.Lock()
	for k, f := range t.flows {
		if f == nil {
			delete(t.flows, k)
			continue
		}
		if now.Sub(f.lastSeen) > idle {
			delete(t.flows, k)
			toClose = append(toClose, f)
		}
	}
	t.mu.Unlock()

	for _, f := range toClose {
		f.closeOnce.Do(func() {
			f.sess.Close()
		})
	}
}

func ipVerFromAddrBytes(b []byte) uint8 {
	if len(b) == 16 {
		return 6
	}
	return 4
}

// ----- TUN open (existing interface created by script) -----

func openExistingTun(name string) (*water.Interface, int, error) {
	if name == "" {
		return nil, 0, fmt.Errorf("tun.device is empty")
	}
	if _, err := net.InterfaceByName(name); err != nil {
		return nil, 0, fmt.Errorf("tun interface %q not found (create it in start script): %w", name, err)
	}

	cfg := water.Config{DeviceType: water.TUN}
	cfg.Name = name
	ifce, err := water.New(cfg)
	if err != nil {
		return nil, 0, fmt.Errorf("open tun %q: %w", name, err)
	}

	ifi, err := net.InterfaceByName(name)
	if err != nil {
		_ = ifce.Close()
		return nil, 0, fmt.Errorf("InterfaceByName(%q): %w", name, err)
	}
	mtu := ifi.MTU
	if mtu <= 0 {
		mtu = 1500
	}
	return ifce, mtu, nil
}

// ----- Main -----

func RunTunNative(ctx context.Context, cfg TunConfig, lb *LoadBalancer) error {
	if !cfg.Enable {
		return nil
	}
	if cfg.Device == "" {
		return fmt.Errorf("tun.device is empty")
	}

	// defaults
	if cfg.UDPMaxFlows == 0 {
		cfg.UDPMaxFlows = 4096
	}
	if cfg.UDPIdleTimeout == 0 {
		cfg.UDPIdleTimeout = 60 * time.Second
	}
	if cfg.UDPGCInterval == 0 {
		cfg.UDPGCInterval = 10 * time.Second
	}

	log.Printf("TUN mode enabled (native), expecting existing interface %q", cfg.Device)

	ifce, mtu, err := openExistingTun(cfg.Device)
	if err != nil {
		return err
	}
	defer ifce.Close()

	log.Printf("TUN opened: %s (mtu=%d)", cfg.Device, mtu)

	st := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})

	ep := channel.New(4096, uint32(mtu), "")
	const nicID tcpip.NICID = 1
	if err := st.CreateNIC(nicID, ep); err != nil {
		return fmt.Errorf("CreateNIC: %w", err)
	}

	// Important tuning for TUN:
	_ = st.SetPromiscuousMode(nicID, true)
	_ = st.SetSpoofing(nicID, true)

	st.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	flowTable := newUDPFlowTable(lb, cfg)

	// UDP GC
	go func() {
		t := time.NewTicker(cfg.UDPGCInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				flowTable.gcOnce()
			}
		}
	}()

	// TCP forwarder
	tcpFwd := tcp.NewForwarder(st, 0, 65535, func(r *tcp.ForwarderRequest) {
		id := r.ID()

		var wq waiter.Queue
		epTCP, err := r.CreateEndpoint(&wq)
		if err != nil {
			r.Complete(true)
			return
		}
		r.Complete(false)

		go tunHandleTCP(ctx, lb, epTCP, id, &wq)
	})
	st.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// UDP forwarder
	udpFwd := udp.NewForwarder(st, func(r *udp.ForwarderRequest) {
		id := r.ID()

		var wq waiter.Queue
		epUDP, err := r.CreateEndpoint(&wq)
		if err != nil {
			return
		}
		go tunHandleUDP(ctx, lb, flowTable, epUDP, id, &wq)
	})
	st.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	// Pumps
	errCh := make(chan error, 2)
	go func() { errCh <- tunToStack(ctx, ifce, ep) }()
	go func() { errCh <- stackToTun(ctx, ifce, ep) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func tunToStack(ctx context.Context, ifce *water.Interface, ep *channel.Endpoint) error {
	buf := make([]byte, 65535)
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		n, err := ifce.Read(buf)
		if err != nil {
			return err
		}
		pkt := buf[:n]

		var proto tcpip.NetworkProtocolNumber
		switch pkt[0] >> 4 {
		case 4:
			proto = ipv4.ProtocolNumber
		case 6:
			proto = ipv6.ProtocolNumber
		default:
			continue
		}

		pb := stack.NewPacketBuffer(stack.PacketBufferOptions{
			Payload: buffer.MakeWithData(append([]byte(nil), pkt...)),
		})
		ep.InjectInbound(proto, pb)
		pb.DecRef()
	}
}

func stackToTun(ctx context.Context, ifce *water.Interface, ep *channel.Endpoint) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		pb := ep.Read()
		if pb == nil {
			time.Sleep(1 * time.Millisecond)
			continue
		}
		v := pb.ToView()
		b := append([]byte(nil), v.AsSlice()...)
		pb.DecRef()

		if _, err := ifce.Write(b); err != nil {
			return err
		}
	}
}

func tunHandleTCP(ctx context.Context, lb *LoadBalancer, epTCP tcpip.Endpoint, id stack.TransportEndpointID, wq *waiter.Queue) {
	defer epTCP.Close()

	nsConn := gonet.NewTCPConn(wq, epTCP)
	defer nsConn.Close()

	dst := net.JoinHostPort(net.IP(id.RemoteAddress.AsSlice()).String(), fmt.Sprintf("%d", id.RemotePort))

	up, err := lb.PickTCP()
	if err != nil {
		return
	}
	out, err := DialOutlineTCP(ctx, lb, up, dst)
	if err != nil {
		lb.ReportTCPFailure(up, err)
		return
	}
	defer out.Close()

	go io.Copy(out, nsConn)
	_, _ = io.Copy(nsConn, out)
}

func tunHandleUDP(ctx context.Context, lb *LoadBalancer, ft *udpFlowTable, epUDP tcpip.Endpoint, id stack.TransportEndpointID, wq *waiter.Queue) {
	defer epUDP.Close()

	nsUDP := gonet.NewUDPConn(wq, epUDP)
	defer nsUDP.Close()

	srcIP := net.IP(id.LocalAddress.AsSlice()).String()
	dstIP := net.IP(id.RemoteAddress.AsSlice()).String()

	key := udpFlowKey{
		netProto: ipVerFromAddrBytes(id.RemoteAddress.AsSlice()),
		srcIP:    srcIP,
		srcPort:  id.LocalPort,
		dstIP:    dstIP,
		dstPort:  id.RemotePort,
	}

	dst := net.JoinHostPort(dstIP, fmt.Sprintf("%d", id.RemotePort))

	// ensure flow exists (per-flow Outline UDP session)
	f := ft.get(key)
	if f == nil {
		up, err := lb.PickUDP()
		if err != nil {
			return
		}
		sess, err := NewOutlineUDPSession(ctx, lb, up)
		if err != nil {
			lb.ReportUDPFailure(up, err)
			return
		}

		f = &udpFlow{
			key:      key,
			dst:      dst,
			up:       up,
			sess:     sess,
			lastSeen: time.Now(),
		}
		if err := ft.put(key, f); err != nil {
			sess.Close()
			return
		}

		// Outline -> netstack (subscribe only our dst)
		replyCh := sess.Subscribe(dst)
		go func(flow *udpFlow) {
			for {
				select {
				case <-ctx.Done():
					return
				case b, ok := <-replyCh:
					if !ok {
						return
					}
					_, _ = nsUDP.Write(b)
					ft.touch(flow.key)
				}
			}
		}(f)
	}

	ft.touch(key)

	// netstack -> Outline
	buf := make([]byte, 65535)
	for {
		n, _, err := nsUDP.ReadFrom(buf)
		if err != nil {
			break
		}
		if n == 0 {
			continue
		}
		if err := f.sess.Send(dst, buf[:n]); err != nil {
			lb.ReportUDPFailure(f.up, err)
			break
		}
		ft.touch(key)
	}

	// cleanup
	if dead := ft.remove(key); dead != nil {
		dead.closeOnce.Do(func() {
			dead.sess.Close()
		})
	}
}
