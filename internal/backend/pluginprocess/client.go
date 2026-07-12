package pluginprocess

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
)

type Config struct {
	Backend string
	Command string
	Args    []string
}

type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	enc    *json.Encoder
	dec    *json.Decoder

	mu     sync.Mutex
	nextID atomic.Uint64
	closed atomic.Bool

	hello hello
}

func Start(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Command == "" {
		return nil, errors.New("backend plugin command is required")
	}
	args := append([]string(nil), cfg.Args...)
	if len(args) == 0 {
		args = []string{"serve"}
	}
	cmd := exec.CommandContext(ctx, cfg.Command, args...) // #nosec G204 - command comes from trusted project metadata.
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("backend plugin stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("backend plugin stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start backend plugin %q: %w", cfg.Command, err)
	}

	c := &Client{
		cmd:    cmd,
		stdin:  stdin,
		stdout: stdout,
		enc:    json.NewEncoder(stdin),
		dec:    json.NewDecoder(stdout),
	}
	if err := c.readHello(cfg.Backend); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Hello() hello {
	return c.hello
}

func (c *Client) request(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", method, err)
	}
	id := fmt.Sprintf("%d", c.nextID.Add(1))
	req := request{ID: id, Method: method, Params: raw}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed.Load() {
		return errors.New("backend plugin client is closed")
	}
	if err := c.enc.Encode(req); err != nil {
		return fmt.Errorf("send %s request: %w", method, err)
	}
	var resp response
	if err := c.dec.Decode(&resp); err != nil {
		return fmt.Errorf("read %s response: %w", method, err)
	}
	if resp.ID != "" && resp.ID != id {
		return fmt.Errorf("backend plugin response id %q does not match request id %q", resp.ID, id)
	}
	if !resp.OK {
		if resp.Error == nil {
			return fmt.Errorf("%s failed", method)
		}
		return fmt.Errorf("%s failed: %s: %s", method, resp.Error.Code, resp.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(resp.Result) == 0 {
		resp.Result = []byte("{}")
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	return nil
}

func (c *Client) readHello(expectedBackend string) error {
	var resp response
	if err := c.dec.Decode(&resp); err != nil {
		return fmt.Errorf("read backend plugin hello: %w", err)
	}
	if !resp.OK {
		if resp.Error == nil {
			return errors.New("backend plugin hello failed")
		}
		return fmt.Errorf("backend plugin hello failed: %s: %s", resp.Error.Code, resp.Error.Message)
	}
	if err := json.Unmarshal(resp.Result, &c.hello); err != nil {
		return fmt.Errorf("decode backend plugin hello: %w", err)
	}
	if c.hello.Protocol != protocolVersion {
		return fmt.Errorf("backend plugin protocol %q, want %q", c.hello.Protocol, protocolVersion)
	}
	if expectedBackend != "" && c.hello.Backend != expectedBackend {
		return fmt.Errorf("backend plugin reports backend %q, want %q", c.hello.Backend, expectedBackend)
	}
	return nil
}

func (c *Client) Close() error {
	if !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	var err error
	if c.stdin != nil {
		err = errors.Join(err, c.stdin.Close())
	}
	if c.stdout != nil {
		err = errors.Join(err, c.stdout.Close())
	}
	if c.cmd != nil {
		err = errors.Join(err, c.cmd.Wait())
	}
	return err
}
