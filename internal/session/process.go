package session

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"time"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func startChild(cmd *exec.Cmd) (*child, error) {
	prepareProcess(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &child{cmd: cmd, done: make(chan struct{})}
	go func() { p.err = cmd.Wait(); close(p.done) }()
	return p, nil
}
func (p *child) stop() {
	select {
	case <-p.done:
		return
	default:
	}
	killProcess(p.cmd)
	<-p.done
}

func freePort() (int, error) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func waitPort(ctx context.Context, address string, p *child) error {
	timeout := time.NewTimer(90 * time.Second)
	defer timeout.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.done:
			return errors.New("connection process exited before its local port became ready")
		case <-timeout.C:
			return errors.New("timed out waiting for the connection; check Azure login, VM access and SSH authentication")
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
			if err == nil {
				conn.Close()
				return nil
			}
		}
	}
}
