// Package bus is the ZeroMQ transport: a SUB subscriber for the vq-core message
// bus and a REQ control client for the SIP track list. Port of vq_db/bus.
//
// Uses the pure-Go go-zeromq/zmq4 so the binary stays CGo-free. The socket is
// isolated behind small structs so it can be swapped for CGo pebbe/zmq4 if
// interop with vq-core's libzmq ever requires it.
package bus

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-zeromq/zmq4"
)

// Subscriber connects to vq-core's PUB socket, subscribes to everything, and
// hands each raw frame to a callback. Runs on its own goroutine and reconnects
// with a backoff on error. Port of vq_db/bus/subscriber.py (ZmqSubscriber).
type Subscriber struct {
	endpoint       string
	onMessage      func([]byte)
	onIdle         func() // called every poll iteration, on the bus goroutine
	pollTimeout    time.Duration
	reconnectDelay time.Duration
	cancel         context.CancelFunc
	done           chan struct{}
	mu             sync.Mutex
	started        bool
}

// NewSubscriber constructs a Subscriber for an explicit endpoint.
func NewSubscriber(endpoint string, onMessage func([]byte), onIdle func()) *Subscriber {
	return &Subscriber{
		endpoint:       endpoint,
		onMessage:      onMessage,
		onIdle:         onIdle,
		pollTimeout:    50 * time.Millisecond,
		reconnectDelay: 5 * time.Second,
	}
}

// SubscriberForPort constructs a Subscriber for tcp://localhost:<port>.
func SubscriberForPort(port int, onMessage func([]byte), onIdle func()) *Subscriber {
	return NewSubscriber(fmt.Sprintf("tcp://localhost:%d", port), onMessage, onIdle)
}

// Start launches the bus goroutine.
func (s *Subscriber) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return fmt.Errorf("already started")
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})
	go s.run(ctx)
	return nil
}

// Stop signals shutdown and waits (up to timeout) for the goroutine to exit.
func (s *Subscriber) Stop(timeout time.Duration) {
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func (s *Subscriber) run(ctx context.Context) {
	defer close(s.done)
	for ctx.Err() == nil {
		sub := zmq4.NewSub(ctx)
		if err := sub.Dial(s.endpoint); err != nil {
			sub.Close()
			s.sleep(ctx, s.reconnectDelay)
			continue
		}
		if err := sub.SetOption(zmq4.OptionSubscribe, ""); err != nil {
			sub.Close()
			s.sleep(ctx, s.reconnectDelay)
			continue
		}
		s.pollLoop(ctx, sub)
		sub.Close()
	}
}

// pollLoop receives frames until ctx is cancelled or a receive error occurs.
// zmq4's Recv blocks (respecting ctx), so the idle callback is driven on a
// separate ticker to preserve the "on_idle every poll iteration" semantics.
func (s *Subscriber) pollLoop(ctx context.Context, sub zmq4.Socket) {
	frames := make(chan [][]byte)
	recvErr := make(chan struct{})
	go func() {
		for {
			msg, err := sub.Recv()
			if err != nil {
				close(recvErr)
				return
			}
			select {
			case frames <- msg.Frames:
			case <-ctx.Done():
				return
			}
		}
	}()

	idle := time.NewTicker(s.pollTimeout)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-recvErr:
			s.sleep(ctx, s.reconnectDelay)
			return
		case fr := <-frames:
			if len(fr) > 0 {
				s.safeOnMessage(fr[0])
			}
			s.safeOnIdle()
		case <-idle.C:
			s.safeOnIdle()
		}
	}
}

func (s *Subscriber) safeOnMessage(frame []byte) {
	defer func() { _ = recover() }() // a bad frame must not kill the loop
	s.onMessage(frame)
}

func (s *Subscriber) safeOnIdle() {
	if s.onIdle == nil {
		return
	}
	defer func() { _ = recover() }() // a failing idle task must not kill the loop
	s.onIdle()
}

func (s *Subscriber) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
