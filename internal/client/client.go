// Package client 是 CLI 与 daemon 的 HTTP 客户端，含自动拉起逻辑。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

// ErrDaemonUnreachable 表示 daemon 无法连接且无法自动拉起。
var ErrDaemonUnreachable = errors.New("client: daemon unreachable")

// Client 封装对 daemon API 的调用。
type Client struct {
	baseURL string
	hc      *http.Client
}

// New 创建 Client。
func New(baseURL string) *Client {
	return &Client{
		baseURL: "http://" + baseURL,
		hc:      &http.Client{Timeout: 10 * time.Second},
	}
}

// Ping 探测 daemon 是否可达。
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/agents", nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	_ = drain(resp.Body)
	return nil
}

// List 返回全部会话。
func (c *Client) List(ctx context.Context) ([]*session.Status, error) {
	var out []*session.Status
	if err := c.getJSON(ctx, "/api/v1/agents", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Start 启动一个会话。
func (c *Client) Start(ctx context.Context, req session.StartRequest) (*session.Status, error) {
	var out session.Status
	if err := c.postJSON(ctx, "/api/v1/agents", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Stop 停止一个会话。
func (c *Client) Stop(ctx context.Context, id string) error {
	return c.delete(ctx, "/api/v1/agents/"+id)
}

// Replay 回放某会话事件。
func (c *Client) Replay(ctx context.Context, id string) ([]store.EventRow, error) {
	var out []store.EventRow
	if err := c.getJSON(ctx, "/api/v1/agents/"+id+"/events", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// EnsureDaemon 确保 daemon 可达；不可达时尝试自动拉起，然后等待就绪。
func (c *Client) EnsureDaemon(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err := c.Ping(probeCtx)
	cancel()
	if err == nil {
		return nil
	}

	// 尝试启动 daemon。
	bin, findErr := findDaemonBin()
	if findErr != nil {
		return fmt.Errorf("%w: %v", ErrDaemonUnreachable, findErr)
	}
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, ".drove", "drove.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("%w: open log: %v", ErrDaemonUnreachable, err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: start daemon: %v", ErrDaemonUnreachable, err)
	}

	// 轮询等待就绪（至多 3 秒）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pctx, pcancel := context.WithTimeout(ctx, 300*time.Millisecond)
		if err := c.Ping(pctx); err == nil {
			pcancel()
			return nil
		}
		pcancel()
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%w: daemon did not become ready (see %s)", ErrDaemonUnreachable, logPath)
}

// -- internal --

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("client: %s %s: %s", resp.Status, path, string(msg))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("client: %s %s: %s", resp.Status, path, string(msg))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	_ = drain(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("client: %s %s", resp.Status, path)
	}
	return nil
}

// findDaemonBin 优先使用 CLI 同目录的 droved，其次 PATH。
func findDaemonBin() (string, error) {
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "droved")
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand, nil
		}
	}
	if p, err := exec.LookPath("droved"); err == nil {
		return p, nil
	}
	return "", errors.New("droved binary not found (build with: make build)")
}

func drain(r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}
