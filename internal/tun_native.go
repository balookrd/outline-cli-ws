package internal

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
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
		// gVisor returns tcpip.Error here (not the built-in error interface),
		// so we can't wrap it with %w.
		return fmt.Errorf("CreateNIC: %v", err)
	}

	// Important tuning for TUN:
	_ = st.SetPromiscuousMode(nicID, true)
	_ = st.SetSpoofing(nicID, true)

	st.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	portTable := newUDPPortTable(lb, cfg)

	go func() {
		t := time.NewTicker(cfg.UDPGCInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				portTable.gcOnce()
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
		go tunHandleUDP(ctx, lb, portTable, epUDP, id, &wq)
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

func tunHandleUDP(ctx context.Context, lb *LoadBalancer, pt *udpPortTable, epUDP tcpip.Endpoint, id stack.TransportEndpointID, wq *waiter.Queue) {
	defer epUDP.Close()

	nsUDP := gonet.NewUDPConn(wq, epUDP)
	defer nsUDP.Close()

	var srcIP netip.Addr
	if id.LocalAddress.Len() == 4 {
		srcIP = netip.AddrFrom4([4]byte(id.LocalAddress.AsSlice()))
	} else {
		srcIP = netip.AddrFrom16([16]byte(id.LocalAddress.AsSlice()))
	}
	var dstAddr netip.Addr
	if id.RemoteAddress.Len() == 4 {
		dstAddr = netip.AddrFrom4([4]byte(id.RemoteAddress.AsSlice()))
	} else {
		dstAddr = netip.AddrFrom16([16]byte(id.RemoteAddress.AsSlice()))
	}
	dst := net.JoinHostPort(dstAddr.String(), fmt.Sprintf("%d", id.RemotePort))

	pk := udpPortKey{
		netProto: 4,
		srcIP:    srcIP,
		srcPort:  id.LocalPort,
	}
	if srcIP.Is6() {
		pk.netProto = 6
	}

	ps, err := pt.getOrCreate(ctx, pk)
	if err != nil {
		return
	}

	// subscribe once per dst inside this port-session
	ps.mu.Lock()

	if _, ok := ps.flows[dst]; !ok {
		maxDst := pt.cfg.UDPMaxDstPerPort
		if maxDst <= 0 {
			maxDst = 512
		}
		if len(ps.flows) >= maxDst {
			ps.mu.Unlock()
			return
		}
		ps.flows[dst] = time.Now()

		dstLocal := dst
		replyCh := ps.sess.Subscribe(dstLocal)
		go func(dst string) {
			defer ps.sess.Unsubscribe(dst)
			for {
				select {
				case <-ctx.Done():
					return
				case p, ok := <-replyCh:
					if !ok {
						return
					}
					_, _ = nsUDP.Write(p.B)
					p.Release()

					now := time.Now()
					ps.mu.Lock()
					ps.flows[dst] = now
					ps.mu.Unlock()

					pt.mu.Lock()
					if cur := pt.ports[pk]; cur != nil {
						cur.lastSeen = now
					}
					pt.mu.Unlock()
				}
			}
		}(dstLocal)
	}
	ps.mu.Unlock()

	// stack -> outline
	buf := make([]byte, 65535)
	for {
		n, _, err := nsUDP.ReadFrom(buf)
		if err != nil {
			break
		}
		if n == 0 {
			continue
		}

		if err := ps.sess.Send(dst, buf[:n]); err != nil {
			lb.ReportUDPFailure(ps.up, err)
			break
		}

		now := time.Now()
		ps.mu.Lock()
		ps.flows[dst] = now
		ps.mu.Unlock()

		pt.mu.Lock()
		if cur := pt.ports[pk]; cur != nil {
			cur.lastSeen = now
		}
		pt.mu.Unlock()
	}

	// optional: remove dst mapping early (GC и так вычистит)
	ps.mu.Lock()
	delete(ps.flows, dst)
	ps.mu.Unlock()
	ps.sess.Unsubscribe(dst)
}
