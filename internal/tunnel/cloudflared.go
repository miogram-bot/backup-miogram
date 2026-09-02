package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var quickTunnelURL = regexp.MustCompile(`https://[-a-zA-Z0-9]+\.trycloudflare\.com`)

type QuickTunnel struct {
	publicURL string
	cancel    context.CancelFunc
	cmd       *exec.Cmd
	done      chan error
	once      sync.Once
}

func StartQuick(parent context.Context, binary, originURL string, timeout time.Duration) (*QuickTunnel, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		binary = "cloudflared"
	}
	originURL = strings.TrimRight(strings.TrimSpace(originURL), "/")
	if originURL == "" {
		return nil, errors.New("origin URL is required")
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}

	ctx, cancel := context.WithCancel(parent)
	cmd := exec.CommandContext(ctx, binary, "tunnel", "--no-autoupdate", "--url", originURL)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}

	urls := make(chan string, 1)
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	go scanOutput(stdout, urls)
	go scanOutput(stderr, urls)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case publicURL := <-urls:
		return &QuickTunnel{
			publicURL: publicURL,
			cancel:    cancel,
			cmd:       cmd,
			done:      done,
		}, nil
	case err := <-done:
		cancel()
		if err == nil {
			err = errors.New("cloudflared exited without a tunnel URL")
		}
		return nil, fmt.Errorf("cloudflared exited before publishing URL: %w", err)
	case <-timer.C:
		cancel()
		waitForExit(cmd, done, 5*time.Second)
		return nil, fmt.Errorf("cloudflared did not publish a tunnel URL within %s", timeout)
	case <-parent.Done():
		cancel()
		waitForExit(cmd, done, 5*time.Second)
		return nil, parent.Err()
	}
}

func (t *QuickTunnel) URL() string {
	if t == nil {
		return ""
	}
	return t.publicURL
}

func (t *QuickTunnel) Close() error {
	if t == nil {
		return nil
	}
	var err error
	t.once.Do(func() {
		t.cancel()
		err = waitForExit(t.cmd, t.done, 5*time.Second)
	})
	return err
}

func OriginURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "http://127.0.0.1:8080"
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	if port, err := strconv.Atoi(addr); err == nil && port > 0 {
		return "http://127.0.0.1:" + addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			return "http://127.0.0.1" + addr
		}
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String()
}

func scanOutput(r io.Reader, urls chan<- string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			log.Printf("cloudflared: %s", line)
		}
		if match := quickTunnelURL.FindString(line); match != "" {
			select {
			case urls <- match:
			default:
			}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("cloudflared output: %v", err)
	}
}

func waitForExit(cmd *exec.Cmd, done <-chan error, timeout time.Duration) error {
	select {
	case err := <-done:
		if err == nil || errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case <-time.After(timeout):
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	}
}
