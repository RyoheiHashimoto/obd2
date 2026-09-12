package can

import (
	"context"
	"fmt"
	"sync"
)

// VirtualBus is an in-memory CAN bus for tests and simulations.
//
// Every frame sent by one endpoint is delivered to every other endpoint.
// All endpoints observe frames in the same order, as on a real bus.
// Endpoint queues are unbounded, so a slow reader never loses frames.
type VirtualBus struct {
	mu    sync.Mutex
	ports []*VirtualPort
}

// NewVirtualBus returns an empty bus.
func NewVirtualBus() *VirtualBus {
	return &VirtualBus{}
}

// Connect attaches a new endpoint to the bus.
func (b *VirtualBus) Connect() *VirtualPort {
	p := &VirtualPort{bus: b, notify: make(chan struct{}, 1)}
	b.mu.Lock()
	b.ports = append(b.ports, p)
	b.mu.Unlock()
	return p
}

// VirtualPort is one endpoint on a VirtualBus. It implements Bus.
type VirtualPort struct {
	bus    *VirtualBus
	notify chan struct{}

	mu     sync.Mutex
	queue  []Frame
	closed bool
}

var _ Bus = (*VirtualPort)(nil)

// Send delivers f to every other endpoint on the bus.
func (p *VirtualPort) Send(ctx context.Context, f Frame) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.Len > MaxDataLen {
		return fmt.Errorf("can: frame length %d exceeds %d", f.Len, MaxDataLen)
	}
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrClosed
	}

	// Holding the bus lock for the whole delivery keeps one total order
	// across concurrent senders.
	p.bus.mu.Lock()
	defer p.bus.mu.Unlock()
	for _, q := range p.bus.ports {
		if q != p {
			q.deliver(f)
		}
	}
	return nil
}

func (p *VirtualPort) deliver(f Frame) {
	p.mu.Lock()
	if !p.closed {
		p.queue = append(p.queue, f)
	}
	p.mu.Unlock()
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// Receive returns the next frame sent by another endpoint.
func (p *VirtualPort) Receive(ctx context.Context) (Frame, error) {
	for {
		p.mu.Lock()
		if len(p.queue) > 0 {
			f := p.queue[0]
			p.queue = p.queue[1:]
			if len(p.queue) == 0 {
				p.queue = nil
			}
			p.mu.Unlock()
			return f, nil
		}
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return Frame{}, ErrClosed
		}
		select {
		case <-p.notify:
		case <-ctx.Done():
			return Frame{}, ctx.Err()
		}
	}
}

// Close detaches the endpoint from the bus. A Receive blocked on it returns
// ErrClosed.
func (p *VirtualPort) Close() error {
	p.bus.mu.Lock()
	for i, q := range p.bus.ports {
		if q == p {
			p.bus.ports = append(p.bus.ports[:i], p.bus.ports[i+1:]...)
			break
		}
	}
	p.bus.mu.Unlock()

	p.mu.Lock()
	p.closed = true
	p.queue = nil
	p.mu.Unlock()
	select {
	case p.notify <- struct{}{}:
	default:
	}
	return nil
}
