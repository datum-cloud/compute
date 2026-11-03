// SPDX-License-Identifier: AGPL-3.0-only

package consoleclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"testing"
)

type rawFrame struct {
	fin     bool
	opcode  byte
	payload []byte
}

func readRawFrame(r *bufio.Reader) (rawFrame, error) {
	var head [2]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return rawFrame{}, err
	}
	f := rawFrame{fin: head[0]&0x80 != 0, opcode: head[0] & 0x0f}
	n := uint64(head[1] & 0x7f)
	switch n {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return rawFrame{}, err
		}
		n = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return rawFrame{}, err
		}
		n = binary.BigEndian.Uint64(ext[:])
	}
	var mask [4]byte
	if head[1]&0x80 != 0 {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return rawFrame{}, err
		}
	}
	f.payload = make([]byte, n)
	if _, err := io.ReadFull(r, f.payload); err != nil {
		return rawFrame{}, err
	}
	for i := range f.payload {
		f.payload[i] ^= mask[i%4]
	}
	return f, nil
}

// The apiserver reads each frame as a whole message, so standard input must
// never reach it as a fragmented message.
func TestStdinMessagesAreNeverFragmented(t *testing.T) {
	client, agent := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = agent.Close() })

	type received struct {
		frames []rawFrame
		err    error
	}
	done := make(chan received, 1)
	go func() {
		r := bufio.NewReader(agent)
		req, err := http.ReadRequest(r)
		if err != nil {
			done <- received{err: err}
			return
		}
		sum := sha1.Sum([]byte(req.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = io.WriteString(agent, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: "+base64.StdEncoding.EncodeToString(sum[:])+"\r\n"+
			"Sec-WebSocket-Protocol: "+req.Header.Get("Sec-WebSocket-Protocol")+"\r\n\r\n")
		var frames []rawFrame
		for {
			f, err := readRawFrame(r)
			if err != nil {
				done <- received{frames: frames, err: err}
				return
			}
			frames = append(frames, f)
			if f.opcode == 0x2 && bytes.Equal(f.payload, []byte{channelClose, channelStdin}) {
				done <- received{frames: frames}
				return
			}
		}
	}()

	stream, err := Start(context.Background(), client, newKey(t), testSessionUID, "agent:7777")
	if err != nil {
		t.Fatal(err)
	}
	input := bytes.Repeat([]byte("0123456789abcdef"), 20000)
	if n, err := stream.Write(input); err != nil || n != len(input) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := stream.CloseStdin(); err != nil {
		t.Fatal(err)
	}

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	var stdin bytes.Buffer
	for _, f := range got.frames {
		if !f.fin || f.opcode != 0x2 {
			t.Fatalf("frame fin=%v opcode=%d; want every message in one binary frame", f.fin, f.opcode)
		}
		if f.payload[0] == channelStdin {
			stdin.Write(f.payload[1:])
		}
	}
	if !bytes.Equal(stdin.Bytes(), input) {
		t.Fatalf("agent received %d bytes of standard input, want %d", stdin.Len(), len(input))
	}
}
