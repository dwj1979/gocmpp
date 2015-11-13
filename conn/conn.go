// Copyright 2015 Tony Bai.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package cmppconn

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"

	cmpppacket "github.com/bigwhite/gocmpp/packet"
)

type Type int8

const (
	V30 Type = 0x30
	V21 Type = 0x21
	V20 Type = 0x20
)

func (t Type) String() string {
	switch {
	case t == V30:
		return "cmpp30"
	case t == V21:
		return "cmpp21"
	case t == V20:
		return "cmpp20"
	default:
		return "unknown"
	}
}

type State uint8

// Conn States
const (
	CONN_CLOSED = iota
	CONN_CONNECTED
	CONN_AUTHOK
)

type Conn struct {
	net.Conn
	State  State
	Typ    Type
	Reader *bufio.Reader
	Writer *bufio.Writer
	SeqId  <-chan uint32
	done   chan<- struct{}
}

func newSeqIdGenerator() (<-chan uint32, chan<- struct{}) {
	out := make(chan uint32)
	done := make(chan struct{})

	go func() {
		var i uint32
		for {
			select {
			case out <- i:
				i++
			case <-done:
				close(out)
				return
			}
		}
	}()
	return out, done
}

func New(conn net.Conn, typ Type) *Conn {
	seqId, done := newSeqIdGenerator()
	c := &Conn{
		Conn:   conn,
		Typ:    typ,
		State:  CONN_CONNECTED,
		Reader: bufio.NewReader(conn),
		Writer: bufio.NewWriter(conn),
		SeqId:  seqId,
		done:   done,
	}
	tc := c.Conn.(*net.TCPConn)
	tc.SetKeepAlive(true)
	return c
}

func (c *Conn) Close() {
	if c != nil {
		if c.Typ == CONN_CLOSED {
			return
		}
		if c.Writer != nil {
			c.Writer.Flush()
		}
		close(c.done)
		c.Conn.Close()
		c.Typ = CONN_CLOSED
		c = nil
	}
}

func (c *Conn) SetState(state State) {
	c.State = state
}

func (c *Conn) writeFull(data []byte) error {
	var written = 0
	for written < len(data) {
		n, err := c.Writer.Write(data[written:])
		if err != nil && err != io.ErrShortWrite {
			return err
		}
		err = c.Writer.Flush()
		if err != nil {
			return err
		}
		written += n
	}

	return nil
}

// SendPkt pack the cmpp packet structure and send it to the other peer.
func (c *Conn) SendPkt(packet cmpppacket.Packer, seqId uint32) error {
	data, err := packet.Pack(seqId)
	if err != nil {
		return err
	}

	err = c.writeFull(data)
	if err != nil {
		return err
	}

	return nil
}

// RecvAndUnpackPkt receives cmpp byte stream, and unpack it to some cmpp packet structure.
func (c *Conn) RecvAndUnpackPkt() (interface{}, error) {
	// Total_Length in packet
	var totalLen uint32
	err := binary.Read(c.Reader, binary.BigEndian, &totalLen)
	if err != nil {
		return nil, err
	}

	if c.Typ == V30 {
		if totalLen < cmpppacket.CMPP3_PACKET_MIN || totalLen > cmpppacket.CMPP3_PACKET_MAX {
			return nil, cmpppacket.ErrTotalLengthInvalid
		}
	}

	if c.Typ == V21 || c.Typ == V20 {
		if totalLen < cmpppacket.CMPP2_PACKET_MIN || totalLen > cmpppacket.CMPP2_PACKET_MAX {
			return nil, cmpppacket.ErrTotalLengthInvalid
		}
	}

	// Command_Id
	var commandId cmpppacket.CommandId
	err = binary.Read(c.Reader, binary.BigEndian, &commandId)
	if err != nil {
		return nil, err
	}

	if !((commandId > cmpppacket.CMPP_REQUEST_MIN && commandId < cmpppacket.CMPP_REQUEST_MAX) ||
		(commandId > cmpppacket.CMPP_RESPONSE_MIN && commandId < cmpppacket.CMPP_RESPONSE_MAX)) {
		return nil, cmpppacket.ErrCommandIdInvalid
	}

	// The left packet data (start from seqId in header).
	var leftData = make([]byte, totalLen-8)
	_, err = io.ReadFull(c.Reader, leftData)
	if err != nil {
		return nil, err
	}

	var p cmpppacket.Packer
	switch commandId {
	case cmpppacket.CMPP_CONNECT:
		p = &cmpppacket.CmppConnReqPkt{}
	case cmpppacket.CMPP_CONNECT_RESP:
		if c.Typ == V30 {
			p = &cmpppacket.Cmpp3ConnRspPkt{}
		} else {
			p = &cmpppacket.Cmpp2ConnRspPkt{}
		}
	case cmpppacket.CMPP_TERMINATE:
		p = &cmpppacket.CmppTerminateReqPkt{}
	case cmpppacket.CMPP_TERMINATE_RESP:
		p = &cmpppacket.CmppTerminateRspPkt{}
	case cmpppacket.CMPP_SUBMIT:
		if c.Typ == V30 {
			p = &cmpppacket.Cmpp3SubmitReqPkt{}
		} else {
			p = &cmpppacket.Cmpp2SubmitReqPkt{}
		}
	case cmpppacket.CMPP_SUBMIT_RESP:
		if c.Typ == V30 {
			p = &cmpppacket.Cmpp3SubmitRspPkt{}
		} else {
			p = &cmpppacket.Cmpp2SubmitRspPkt{}
		}
	case cmpppacket.CMPP_DELIVER:
		if c.Typ == V30 {
			p = &cmpppacket.Cmpp3DeliverReqPkt{}
		} else {
			p = &cmpppacket.Cmpp2DeliverReqPkt{}
		}
	case cmpppacket.CMPP_DELIVER_RESP:
		if c.Typ == V30 {
			p = &cmpppacket.Cmpp3DeliverRspPkt{}
		} else {
			p = &cmpppacket.Cmpp2DeliverRspPkt{}
		}
	case cmpppacket.CMPP_FWD:
		if c.Typ == V30 {
			p = &cmpppacket.Cmpp3FwdReqPkt{}
		} else {
			p = &cmpppacket.Cmpp2FwdReqPkt{}
		}
	case cmpppacket.CMPP_FWD_RESP:
		if c.Typ == V30 {
			p = &cmpppacket.Cmpp3FwdRspPkt{}
		} else {
			p = &cmpppacket.Cmpp2FwdRspPkt{}
		}
	case cmpppacket.CMPP_ACTIVE_TEST:
		p = &cmpppacket.CmppActiveTestReqPkt{}
	case cmpppacket.CMPP_ACTIVE_TEST_RESP:
		p = &cmpppacket.CmppActiveTestRspPkt{}

	default:
		p = nil
		return nil, cmpppacket.ErrCommandIdNotSupported
	}

	err = p.Unpack(leftData)
	if err != nil {
		return nil, err
	}
	return p, nil
}
