// A Client is one viewer's real tmux client, running on its own pty in this process.
//
// Because it is a real client, tmux paints it completely when it attaches and after every resize,
// and keeps its own view of the screen: a viewer joining late, or one that fell behind, gets a full
// picture without anybody reconstructing one. Each viewer has its own client, so one viewer's
// input, size or slowness never reaches another's stream.
//
// Flow control keeps a slow viewer from queueing without bound. The reader stops taking bytes from
// the pty once window bytes are sent and not yet acknowledged with Ack; tmux then sees a client
// that cannot keep up, stops sending it every change, and redraws it whole when it drains.
//
//	(*Session).Attach(cols, rows, window int, onData func([]byte), onEnd func(error)) (*Client, error)
//	    Starts `tmux attach-session` on a pty of cols x rows. onData receives the client's output
//	    in order; onEnd is called once when the client ends for any reason other than Close.
//	    window is the flow-control limit in bytes (Window is the usual one); 0 turns it off.
//	(*Client).Write(p []byte) error    the viewer's input: keys, pastes and mouse sequences.
//	(*Client).Resize(cols, rows) error sets the pty size; call it with the window's size.
//	(*Client).Ack(n int)               n more bytes have been rendered by the viewer.
//	(*Client).Close()                  detaches; the session keeps running.
//
// Example:
//
//	c, err := s.Attach(120, 32, terminal.Window, func(b []byte) { send(b) }, func(err error) { log.Print(err) })
//	err = c.Write([]byte("ls\r"))
//	c.Ack(len(received))
package terminal

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

// Window is the usual flow-control limit: how many bytes a viewer may have unacknowledged.
const Window = 256 << 10

// Client is one attached viewer.
type Client struct {
	ptmx *os.File
	cmd  *exec.Cmd

	window  int
	mu      sync.Mutex
	cond    *sync.Cond
	unacked int
	closed  bool
	once    sync.Once
}

func (s *Session) Attach(cols, rows, window int, onData func([]byte), onEnd func(error)) (*Client, error) {
	if cols <= 0 || rows <= 0 {
		return nil, fmt.Errorf("terminal: attach %s: size %dx%d", s.name, cols, rows)
	}
	cmd := s.m.tmux("attach-session", "-t", "="+s.name)
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("terminal: attach %s: %w", s.name, err)
	}
	c := &Client{ptmx: ptmx, cmd: cmd, window: window}
	c.cond = sync.NewCond(&c.mu)
	go c.read(onData, onEnd)
	return c, nil
}

func (c *Client) read(onData func([]byte), onEnd func(error)) {
	buf := make([]byte, 32<<10)
	for {
		c.mu.Lock()
		for c.window > 0 && c.unacked >= c.window && !c.closed {
			c.cond.Wait()
		}
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return
		}
		n, err := c.ptmx.Read(buf)
		if n > 0 {
			c.mu.Lock()
			c.unacked += n
			c.mu.Unlock()
			onData(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				onEnd(fmt.Errorf("terminal: viewer client ended: %w", err))
			}
			return
		}
	}
}

func (c *Client) Ack(n int) {
	c.mu.Lock()
	c.unacked -= n
	if c.unacked < 0 {
		c.unacked = 0
	}
	c.mu.Unlock()
	c.cond.Broadcast()
}

func (c *Client) Write(p []byte) error {
	_, err := c.ptmx.Write(p)
	return err
}

func (c *Client) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("terminal: resize client: size %dx%d", cols, rows)
	}
	return pty.Setsize(c.ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}

func (c *Client) Close() {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.mu.Unlock()
		c.cond.Broadcast()
		c.ptmx.Close()
		if c.cmd.Process != nil {
			c.cmd.Process.Kill()
		}
		c.cmd.Wait()
	})
}
