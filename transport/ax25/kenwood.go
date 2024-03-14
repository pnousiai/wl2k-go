// Copyright 2015 Martin Hebnes Pedersen (LA5NTA). All rights reserved.
// Use of this source code is governed by the MIT-license that can be
// found in the LICENSE file.

package ax25

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/albenik/go-serial/v2"
	"github.com/pnousiai/wl2k-go/fbb"
)

// KenwoodConn implements net.Conn using a
// Kenwood (or similar) TNC in connected transparent mode.
//
// Tested with Kenwood TH-D72 and TM-D710 in "packet-mode".
//
// TODO: github.com/term/goserial does not support setting the
// line flow control. Thus, KenwoodConn is not suitable for
// sending messages > the TNC's internal buffer size.
//
// We should probably be using software flow control (XFLOW),
// as hardware flow is not supported by many USB->RS232 adapters
// including the adapter build into TH-D72 (at least, not using the
// current linux kernel module.
type KenwoodConn struct{ Conn }

// Dial a packet node using a Kenwood (or similar) radio over serial
func DialKenwood(dev, mycall, targetcall string, config Config, logger *log.Logger) (*KenwoodConn, error) {
	if logger == nil {
		logger = log.New(os.Stderr, "", log.LstdFlags)
	}

	localAddr, remoteAddr := tncAddrFromString(mycall), tncAddrFromString(targetcall)
	conn := &KenwoodConn{Conn{
		localAddr:  AX25Addr{localAddr},
		remoteAddr: AX25Addr{remoteAddr},
	}}

	if dev == "socket" {
		c, err := net.Dial("tcp", "127.0.0.1:8081")
		if err != nil {
			panic(err)
		}
		conn.Conn.ReadWriteCloser = c
	} else {
		s, err := serial.Open(dev, serial.WithBaudrate(config.SerialBaud))
		if err != nil {
			return conn, err
		} else {
			conn.Conn.ReadWriteCloser = s
		}
	}

	// Initialize the TNC (with timeout)
	initErr := make(chan error, 1)
	go func() {
		defer close(initErr)
		conn.Write([]byte{3, 3, 3}) // ETX
		fmt.Fprint(conn, "\r\nrestart\r\n")
		// Wait for prompt, then send all the init commands
		for {
			line, err := fbb.ReadLine(conn)
			if err != nil {
				conn.Close()
				initErr <- err
				return
			}

			if strings.HasPrefix(line, "cmd:") {
				fmt.Fprint(conn, "ECHO OFF\r") // Don't echo commands
				fmt.Fprint(conn, "FLOW OFF\r")
				fmt.Fprint(conn, "XFLOW ON\r")    // Enable software flow control
				fmt.Fprint(conn, "LFIGNORE ON\r") // Ignore linefeed (\n)
				fmt.Fprint(conn, "AUTOLF OFF\r")  // Don't auto-insert linefeed
				fmt.Fprint(conn, "CR ON\r")
				fmt.Fprint(conn, "8BITCONV ON\r") // Use 8-bit characters

				// Return to command mode if station of current I/O stream disconnects.
				fmt.Fprint(conn, "NEWMODE ON\r")

				time.Sleep(500 * time.Millisecond)

				fmt.Fprintf(conn, "MYCALL %s\r", mycall)
				fmt.Fprintf(conn, "HBAUD %d\r", config.HBaud)
				fmt.Fprintf(conn, "PACLEN %d\r", config.PacketLength)
				fmt.Fprintf(conn, "TXDELAY %d\r", config.TXDelay/_CONFIG_TXDELAY_UNIT)
				fmt.Fprintf(conn, "PERSIST %d\r", config.Persist)
				time.Sleep(500 * time.Millisecond)

				fmt.Fprintf(conn, "SLOTTIME %d\r", config.SlotTime/_CONFIG_SLOT_TIME_UNIT)
				fmt.Fprint(conn, "FULLDUP OFF\r")
				fmt.Fprintf(conn, "MAXFRAME %d\r", config.MaxFrame)
				fmt.Fprintf(conn, "FRACK %d\r", config.FRACK/_CONFIG_FRACK_UNIT)
				fmt.Fprintf(conn, "RESPTIME %d\r", config.ResponseTime/_CONFIG_RESPONSE_TIME_UNIT)
				fmt.Fprintf(conn, "NOMODE ON\r")

				break
			}
		}
	}()
	select {
	case <-time.After(3 * time.Second):
		conn.Close()
		return nil, fmt.Errorf("initialization failed: deadline exceeded")
	case err := <-initErr:
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("initialization failed: %w", err)
		}
	}
	time.Sleep(2 * time.Second)

	// Dial the connection (with timeout)
	dialErr := make(chan error, 1)
	go func() {
		defer close(dialErr)
		fmt.Fprintf(conn, "\rc %s\r", targetcall)
		// Wait for connect acknowledgement
		for {
			line, err := fbb.ReadLine(conn)
			if err != nil {
				dialErr <- err
				return
			}
			logger.Println(line)
			line = strings.TrimSpace(line)

			switch {
			case strings.Contains(line, "*** DISCONNECTED"):
				dialErr <- fmt.Errorf("got disconnect %d", int(line[len(line)-1]))
				return
			case strings.Contains(line, "*** CONNECTED to"):
				return
			}
		}
	}()
	select {
	case <-time.After(5 * time.Minute):
		conn.Close()
		return nil, fmt.Errorf("connect failed: deadline exceeded")
	case err := <-initErr:
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("connect failed: %w", err)
		}
	}

	// Success! Switch to TRANSPARENT mode and return the connection
	fmt.Fprint(conn, "TRANS\r\n")
	return conn, nil
}

func (c *KenwoodConn) Close() error {
	if !c.ok() {
		return syscall.EINVAL
	}

	// Exit TRANS mode
	time.Sleep(1 * time.Second)
	for i := 0; i < 3; i++ {
		c.Write([]byte{3}) // ETX
		time.Sleep(200 * time.Millisecond)
	}

	// Wait for prompt
	time.Sleep(1 * time.Second)

	// Disconnect
	fmt.Fprint(c, "\r\nD\r\n")
	for {
		line, _ := fbb.ReadLine(c)
		if strings.Contains(line, `DISCONN`) {
			log.Println(`Disconnected`)
			break
		}
	}
	return c.Conn.Close()
}
