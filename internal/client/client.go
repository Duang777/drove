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
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Duang777/drove/internal/auth"
	"github.com/Duang777/drove/internal/recording"
	"github.com/Duang777/drove/internal/session"
	"github.com/Duang777/drove/internal/store"
)

var (
	// ErrDaemonUnreachable 表示 daemon 无法连接且无法自动拉起。
	ErrDaemonUnreachable = errors.New("client: daemon unreachable")
	// ErrUnauthorized 表示 daemon 拒绝了本地控制凭据。
	ErrUnauthorized = errors.New("client: daemon rejected control token")
)

// Client 封装对 daemon API 的调用。
type Client struct {
	baseURL   string
	hc        *http.Client
	tokenPath string
}

// Option configures a Client.
type Option func(*Client)

// WithTokenFile configures the control token file read before each request.
func WithTokenFile(path string) Option {
	return func(client *Client) {
		client.tokenPath = path
	}
}

// New 创建 Client。
func New(baseURL string, options ...Option) *Client {
	client := &Client{
		baseURL: "http://" + baseURL,
		hc:      &http.Client{Timeout: 10 * time.Second},
	}
	for _, option := range options {
		option(client)
	}
	return client
}

// Ping 探测 daemon 是否可达。
func (c *Client) Ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/agents", nil)
	if err != nil {
		return err
	}
	if err := c.authorize(req, true); err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	if err := c.responseError(resp, "/api/v1/agents"); err != nil {
		return err
	}
	return drain(resp.Body)
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

type inputRequest struct {
	Data string `json:"data"`
}

// SendInput 向一个已连接的 Agent 发送 UTF-8 文本。
func (c *Client) SendInput(ctx context.Context, id string, data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("client: input is not valid UTF-8")
	}
	return c.postJSON(
		ctx,
		"/api/v1/agents/"+url.PathEscape(id)+"/input",
		inputRequest{Data: string(data)},
		nil,
	)
}

// Replay 回放某会话事件。
func (c *Client) Replay(ctx context.Context, id string) ([]store.EventRow, error) {
	var out []store.EventRow
	if err := c.getJSON(ctx, "/api/v1/agents/"+id+"/events", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Timeline returns the captured state and output-retention projection.
func (c *Client) Timeline(
	ctx context.Context,
	id string,
) (*recording.Timeline, error) {
	var out recording.Timeline
	if err := c.getJSON(
		ctx,
		"/api/v1/agents/"+url.PathEscape(id)+"/timeline",
		&out,
	); err != nil {
		return nil, err
	}
	return &out, nil
}

// BlockedOccurrence returns one one-based Blocked interval and lead-in cursor.
func (c *Client) BlockedOccurrence(
	ctx context.Context,
	id string,
	number int,
) (*recording.BlockedOccurrence, error) {
	if number <= 0 {
		return nil, fmt.Errorf(
			"%w: %d",
			recording.ErrInvalidBlockedOccurrence,
			number,
		)
	}
	path := fmt.Sprintf(
		"/api/v1/agents/%s/timeline/blocked/%d",
		url.PathEscape(id),
		number,
	)
	var out recording.BlockedOccurrence
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Frame returns one exact bounded terminal frame.
func (c *Client) Frame(
	ctx context.Context,
	id string,
	selector recording.Selector,
) (*recording.Frame, error) {
	query := url.Values{}
	switch selector.Kind() {
	case recording.SelectorSequence:
		sequence, _ := selector.Sequence()
		query.Set("seq", sequence.String())
	case recording.SelectorTime:
		at, _ := selector.Time()
		query.Set("at", at.UTC().Format(time.RFC3339Nano))
	case recording.SelectorOutputOffset:
		offset, _ := selector.OutputOffset()
		query.Set("offset", offset.String())
	default:
		return nil, recording.ErrInvalidFrameSelector
	}
	path := "/api/v1/agents/" + url.PathEscape(id) + "/frame?" + query.Encode()
	var out recording.Frame
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Explain returns the daemon's typed, bounded explanation for one Agent.
func (c *Client) Explain(
	ctx context.Context,
	id string,
	options session.ExplainOptions,
) (*session.Explanation, error) {
	path := "/api/v1/agents/" + url.PathEscape(id) + "/explain"
	if options.Limit != 0 {
		query := url.Values{}
		query.Set("limit", fmt.Sprintf("%d", options.Limit))
		path += "?" + query.Encode()
	}
	var out session.Explanation
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EnsureDaemon 确保 daemon 可达；不可达时使用同一配置自动拉起，然后等待就绪。
func (c *Client) EnsureDaemon(ctx context.Context, configPath string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	err := c.Ping(probeCtx)
	cancel()
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrDaemonUnreachable) {
		return err
	}

	// 尝试启动 daemon。
	bin, findErr := findDaemonBin()
	if findErr != nil {
		return fmt.Errorf("%w: %v", ErrDaemonUnreachable, findErr)
	}
	logPath := c.daemonLogPath()
	logFile, err := openDaemonLog(logPath)
	if err != nil {
		return fmt.Errorf("%w: open log: %v", ErrDaemonUnreachable, err)
	}
	defer logFile.Close()

	cmd := exec.Command(bin, daemonArgs(configPath)...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: start daemon: %v", ErrDaemonUnreachable, err)
	}

	// 轮询等待就绪（至多 3 秒）。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pctx, pcancel := context.WithTimeout(ctx, 300*time.Millisecond)
		pingErr := c.Ping(pctx)
		if pingErr == nil {
			pcancel()
			return nil
		}
		pcancel()
		if !errors.Is(pingErr, ErrDaemonUnreachable) {
			return pingErr
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%w: daemon did not become ready (see %s)", ErrDaemonUnreachable, logPath)
}

func (c *Client) daemonLogPath() string {
	if c.tokenPath != "" {
		return filepath.Join(filepath.Dir(c.tokenPath), "drove.log")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".drove", "drove.log")
}

func openDaemonLog(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// -- internal --

func daemonArgs(configPath string) []string {
	if configPath == "" {
		return nil
	}
	return []string{"--config", configPath}
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if err := c.authorize(req, false); err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	if err := c.responseError(resp, path); err != nil {
		return err
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
	if err := c.authorize(req, false); err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	if err := c.responseError(resp, path); err != nil {
		return err
	}
	if out == nil {
		return drain(resp.Body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) delete(ctx context.Context, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if err := c.authorize(req, false); err != nil {
		return err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return ErrDaemonUnreachable
	}
	defer resp.Body.Close()
	if err := c.responseError(resp, path); err != nil {
		return err
	}
	return drain(resp.Body)
}

func (c *Client) authorize(req *http.Request, allowMissing bool) error {
	if c.tokenPath == "" {
		return nil
	}
	token, err := auth.Read(c.tokenPath)
	if err != nil {
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("client: read control token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (c *Client) responseError(resp *http.Response, path string) error {
	if resp.StatusCode < http.StatusBadRequest {
		return nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%w: %s", ErrUnauthorized, strings.TrimSpace(string(msg)))
	}
	if resp.StatusCode == http.StatusGone {
		var payload struct {
			Code      string                  `json:"code"`
			SessionID string                  `json:"session_id"`
			Missing   []recording.OutputRange `json:"missing"`
		}
		if json.Unmarshal(msg, &payload) == nil && payload.Code == "output_expired" {
			return &recording.OutputExpiredError{
				SessionID: payload.SessionID,
				Missing:   payload.Missing,
			}
		}
	}
	return fmt.Errorf("client: %s %s: %s", resp.Status, path, string(msg))
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
