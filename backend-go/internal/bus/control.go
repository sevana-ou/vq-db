package bus

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-zeromq/zmq4"
	"google.golang.org/protobuf/proto"

	pb "github.com/sevana-ou/vq-db/internal/proto"
)

// TrackOp is a track-list control operation.
type TrackOp int

const (
	TrackAdd TrackOp = iota
	TrackRemove
	TrackReplace
	TrackQuery
)

func (op TrackOp) protoOp() pb.Command_Op {
	switch op {
	case TrackAdd:
		return pb.Command_TRACK_ADD
	case TrackRemove:
		return pb.Command_TRACK_REMOVE
	case TrackReplace:
		return pb.Command_TRACK_REPLACE
	default:
		return pb.Command_TRACK_QUERY
	}
}

// TrackAck is the result of a track-list command. TransportOK is false when
// vq-core's control socket did not reply (down / wrong port / timeout); OK
// reflects the server's CommandAck.ok when a reply was received.
type TrackAck struct {
	TransportOK     bool
	OK              bool
	Error           string
	Current         []string
	AffectedStreams uint32
}

// ControlClient is a lock-step REQ/REP client to vq-core's control socket,
// serialized by a mutex and recreating the socket after a missed reply (the
// classic REQ-stuck-in-recv gotcha). Port of vq_db/bus/control_client.py.
type ControlClient struct {
	endpoint string
	timeout  time.Duration
	mu       sync.Mutex
	socket   zmq4.Socket
}

// NewControlClient constructs and connects a ControlClient.
func NewControlClient(endpoint string, timeout time.Duration) (*ControlClient, error) {
	c := &ControlClient{endpoint: endpoint, timeout: timeout}
	if err := c.openSocket(); err != nil {
		return nil, err
	}
	return c, nil
}

// ControlClientForPort constructs a ControlClient for tcp://localhost:<port>.
func ControlClientForPort(port int, timeout time.Duration) (*ControlClient, error) {
	return NewControlClient(fmt.Sprintf("tcp://localhost:%d", port), timeout)
}

func (c *ControlClient) openSocket() error {
	if c.socket != nil {
		c.socket.Close()
	}
	c.socket = zmq4.NewReq(context.Background())
	return c.socket.Dial(c.endpoint)
}

// Close closes the underlying socket.
func (c *ControlClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.socket != nil {
		c.socket.Close()
		c.socket = nil
	}
}

// Send performs one track-list command and returns the ack.
func (c *ControlClient) Send(op TrackOp, patterns []string) TrackAck {
	cmd := &pb.Command{Op: op.protoOp()}
	for _, p := range patterns {
		cmd.Entries = append(cmd.Entries, &pb.TrackEntry{SipPattern: p})
	}
	buffer, err := proto.Marshal(cmd)
	if err != nil {
		return TrackAck{Error: err.Error()}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var result TrackAck
	if err := c.socket.Send(zmq4.NewMsg(buffer)); err != nil {
		c.openSocket()
		result.Error = err.Error()
		return result
	}

	reply, err := c.recvWithTimeout()
	if err != nil {
		// No reply within the timeout: the REQ socket is now stuck in recv and
		// must be recreated before the next request.
		c.openSocket()
		result.Error = "control socket did not reply"
		return result
	}

	ack := &pb.CommandAck{}
	if err := proto.Unmarshal(reply, ack); err != nil {
		result.TransportOK = true
		result.Error = "failed to parse CommandAck"
		return result
	}
	result.TransportOK = true
	result.OK = ack.GetOk()
	result.Error = ack.GetError()
	result.AffectedStreams = ack.GetAffectedStreams()
	for _, e := range ack.GetCurrent() {
		result.Current = append(result.Current, e.GetSipPattern())
	}
	return result
}

// recvWithTimeout receives one reply, giving up after the configured timeout.
func (c *ControlClient) recvWithTimeout() ([]byte, error) {
	type res struct {
		msg zmq4.Msg
		err error
	}
	ch := make(chan res, 1)
	go func() {
		msg, err := c.socket.Recv()
		ch <- res{msg, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if len(r.msg.Frames) == 0 {
			return nil, fmt.Errorf("empty reply")
		}
		return r.msg.Frames[0], nil
	case <-time.After(c.timeout):
		return nil, fmt.Errorf("timeout")
	}
}
