// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
)

// MaxFrameSize is the largest WebSocket frame the agent relays. A larger frame
// ends the session.
const MaxFrameSize = 16 << 20

const (
	opContinuation = 0x0
	opText         = 0x1
	opBinary       = 0x2
	opClose        = 0x8
	opPing         = 0x9
	opPong         = 0xa

	closeNormal = 1000

	channelStdin  = 0
	channelStdout = 1
	channelStderr = 2
	channelStatus = 3
)

// frame is one WebSocket frame. raw holds the frame exactly as it arrived, so
// it can be relayed untouched; payload is its unmasked payload.
type frame struct {
	fin     bool
	opcode  byte
	payload []byte
	raw     []byte
}

func (f *frame) control() bool {
	return f.opcode >= opClose
}

// errFrameTooLarge reports a frame over MaxFrameSize.
type errFrameTooLarge struct {
	size uint64
}

func (e errFrameTooLarge) Error() string {
	return fmt.Sprintf("frame of %d bytes exceeds the %d byte limit", e.size, MaxFrameSize)
}

// here be dragons
func readFrame(r *bufio.Reader) (*frame, error) {
	head := make([]byte, 2, 14)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, err
	}
	length := uint64(head[1] & 0x7f)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		head = append(head, ext...)
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(r, ext); err != nil {
			return nil, err
		}
		head = append(head, ext...)
		length = binary.BigEndian.Uint64(ext)
	}
	var mask []byte
	if head[1]&0x80 != 0 {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(r, mask); err != nil {
			return nil, err
		}
		head = append(head, mask...)
	}
	if length > MaxFrameSize {
		return nil, errFrameTooLarge{size: length}
	}
	raw := make([]byte, len(head)+int(length))
	copy(raw, head)
	if _, err := io.ReadFull(r, raw[len(head):]); err != nil {
		return nil, err
	}
	payload := raw[len(head):]
	if mask != nil {
		payload = make([]byte, length)
		for i := range payload {
			payload[i] = raw[len(head)+i] ^ mask[i%4]
		}
	}
	return &frame{fin: head[0]&0x80 != 0, opcode: head[0] & 0x0f, payload: payload, raw: raw}, nil
}

// encodeFrame builds a single final frame. Frames a client sends must be
// masked; frames a server sends must not.
func encodeFrame(opcode byte, payload []byte, masked bool) []byte {
	out := []byte{0x80 | opcode}
	maskBit := byte(0)
	if masked {
		maskBit = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		out = append(out, maskBit|byte(n))
	case n <= 0xffff:
		out = append(out, maskBit|126)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
	default:
		out = append(out, maskBit|127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	if !masked {
		return append(out, payload...)
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	out = append(out, mask...)
	for i, b := range payload {
		out = append(out, b^mask[i%4])
	}
	return out
}

func closePayload(code uint16) []byte {
	return binary.BigEndian.AppendUint16(nil, code)
}
