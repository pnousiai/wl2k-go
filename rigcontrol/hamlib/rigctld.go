// Copyright 2015 Martin Hebnes Pedersen (LA5NTA). All rights reserved.
// Use of this source code is governed by the MIT-license that can be
// found in the LICENSE file.

package hamlib

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultTCPAddr = "localhost:4532"

var ErrNotVFOMode = errors.New("rigctl is not running in VFO mode")

var ErrUnexpectedValue = fmt.Errorf("Unexpected value in response")

// TCPTimeout defines the timeout duration of dial, read and write operations.
var TCPTimeout = 30 * time.Second

// Rig represents a receiver or tranceiver.
//
// It holds the tcp connection to the service (rigctld).
type TCPRig struct {
	mu      sync.Mutex
	conn    *textproto.Conn
	tcpConn net.Conn
	addr    string
}

// VFO (Variable Frequency Oscillator) represents a tunable channel,
// from the radio operator's view.
//
// Also referred to as "BAND" (A-band/B-band) by some radio manufacturers.
type tcpVFO struct {
	r      *TCPRig
	prefix string
}

// OpenTCP opens a new TCPRig and returns a ready to use Rig.
//
// The connection to rigctld is not initiated until the connection is requred.
// To check for a valid connection, call Ping.
//
// Caller must remember to Close the Rig after use.
func OpenTCP(addr string) (*TCPRig, error) {
	r := &TCPRig{addr: addr}
	return r, nil
}

// Ping checks that a connection to rigctld is open and valid.
//
// If no connection is active, it will try to establish one.
func (r *TCPRig) Ping() error { _, err := r.cmd(`dump_caps`); return err }

func (r *TCPRig) dial() (err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conn != nil {
		r.conn.Close()
	}

	// Dial with 3 second timeout
	fmt.Printf("Koitetaan")
	return err
	r.tcpConn, err = net.DialTimeout("tcp", r.addr, TCPTimeout)
	fmt.Printf("Juuri tuli %s", err)
	if err != nil {
		return err
	}

	r.conn = textproto.NewConn(r.tcpConn)

	return err
}

// Closes the connection to the Rig.
func (r *TCPRig) Close() error {
	if r.conn == nil {
		return nil
	}
	return r.conn.Close()
}

// Returns the Rig's active VFO (for control).
func (r *TCPRig) CurrentVFO() VFO { return &tcpVFO{r, ""} }

// Returns the Rig's VFO A (for control).
//
// ErrNotVFOMode is returned if rigctld is not in VFO mode.
func (r *TCPRig) VFOA() (VFO, error) {
	if ok, err := r.VFOMode(); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotVFOMode
	}

	return &tcpVFO{r, "VFOA"}, nil
}

// Returns the Rig's VFO B (for control).
//
// ErrNotVFOMode is returned if rigctld is not in VFO mode.
func (r *TCPRig) VFOB() (VFO, error) {
	if ok, err := r.VFOMode(); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotVFOMode
	}

	return &tcpVFO{r, "VFOB"}, nil
}

func (r *TCPRig) VFOMode() (bool, error) {
	resp, err := r.cmd(`\chk_vfo`)
	if err != nil {
		return false, err
	}
	return strings.TrimPrefix(resp, "CHKVFO ") == "1", nil
}

// Gets the dial frequency for this VFO.
func (v *tcpVFO) GetFreq() (int, error) {
	//Printf("Missing address-field for rig '%s', skipping.", int)
	resp, err := v.cmd(`\get_freq`)
	if err != nil {
		return -1, err
	}

	freq, err := strconv.Atoi(resp)
	if err != nil {
		return -1, err
	}

	return freq, nil
}

// Sets the dial frequency for this VFO.
func (v *tcpVFO) SetFreq(freq int) error {
	_, err := v.cmd(`\set_freq %d`, freq)
	return err
}

// GetPTT returns the PTT state for this VFO.
func (v *tcpVFO) GetPTT() (bool, error) {
	resp, err := v.cmd("t")
	if err != nil {
		return false, err
	}

	switch resp {
	case "0":
		return false, nil
	case "1", "2", "3":
		return true, nil
	default:
		return false, ErrUnexpectedValue
	}
}

// Enable (or disable) PTT on this VFO.
func (v *tcpVFO) SetPTT(on bool) error {
	bInt := 0
	if on == true {
		bInt = 1
	}

	// Experimental PTT STATE 3 (https://github.com/la5nta/pat/issues/184)
	if experimentalPTT3Enabled() {
		bInt = 3
	}

	_, err := v.cmd(`\set_ptt %d`, bInt)
	return err
}

func (v *tcpVFO) cmd(format string, args ...interface{}) (string, error) {
	// Add VFO argument (if set)
	if v.prefix != "" {
		parts := strings.Split(format, " ")
		parts = append([]string{parts[0], v.prefix}, parts[1:]...)
		format = strings.Join(parts, " ")
	}
	return v.r.cmd(format, args...)
}

func (r *TCPRig) cmd(format string, args ...interface{}) (resp string, err error) {
	// Retry
	for i := 0; i < 3; i++ {
		if r.conn == nil {
			// Try re-dialing
			if err = r.dial(); err != nil {
				break
			}
		}

		resp, err = r.doCmd(format, args...)
		if err == nil {
			break
		}

		_, isNetError := err.(net.Error)
		if err == io.EOF || isNetError {
			r.conn = nil
		}
	}
	return resp, err
}

func (r *TCPRig) doCmd(format string, args ...interface{}) (string, error) {
	r.tcpConn.SetDeadline(time.Now().Add(TCPTimeout))
	id, err := r.conn.Cmd(format, args...)
	r.tcpConn.SetDeadline(time.Time{})

	if err != nil {
		return "", err
	}

	r.conn.StartResponse(id)
	defer r.conn.EndResponse(id)

	r.tcpConn.SetDeadline(time.Now().Add(TCPTimeout))
	resp, err := r.conn.ReadLine()
	r.tcpConn.SetDeadline(time.Time{})

	if err != nil {
		return "", err
	} else if err := toError(resp); err != nil {
		return resp, err
	}

	return resp, nil
}

func toError(str string) error {
	if !strings.HasPrefix(str, "RPRT ") {
		return nil
	}

	parts := strings.SplitN(str, " ", 2)

	code, err := strconv.Atoi(parts[1])
	if err != nil {
		return err
	}

	switch code {
	case 0:
		return nil
	default:
		return fmt.Errorf("code %d", code)
	}
}
