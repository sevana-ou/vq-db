package bus

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/go-zeromq/zmq4"
	"google.golang.org/protobuf/proto"

	pb "github.com/sevana-ou/vq-db/internal/proto"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// vq-db can start before vq-core listens (both come up at boot): the client
// must still be usable and connect once vq-core is there.
func TestControlClientDialsLateServer(t *testing.T) {
	port := freePort(t)
	endpoint := fmt.Sprintf("tcp://127.0.0.1:%d", port)

	c, err := NewControlClient(endpoint, 2*time.Second)
	if err == nil {
		t.Fatal("expected a dial error with nothing listening")
	}
	if c == nil {
		t.Fatal("client must be returned even when the first dial fails")
	}
	defer c.Close()
	if ack := c.Send(TrackQuery, nil); ack.TransportOK {
		t.Fatal("Send must fail while vq-core is down")
	}

	rep := zmq4.NewRep(context.Background())
	defer rep.Close()
	if err := rep.Listen(endpoint); err != nil {
		t.Fatal(err)
	}
	go func() {
		if _, err := rep.Recv(); err != nil {
			return
		}
		ack := &pb.CommandAck{Ok: true, Current: []*pb.TrackEntry{{SipPattern: "sip:"}}}
		buf, _ := proto.Marshal(ack)
		rep.Send(zmq4.NewMsg(buf))
	}()

	ack := c.Send(TrackQuery, nil)
	if !ack.TransportOK || !ack.OK {
		t.Fatalf("Send after vq-core came up: %+v", ack)
	}
	if len(ack.Current) != 1 || ack.Current[0] != "sip:" {
		t.Fatalf("unexpected current list: %v", ack.Current)
	}
}
