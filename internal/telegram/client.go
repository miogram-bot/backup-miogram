package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/net/proxy"
)

type Client struct {
	token       string
	baseURL     string
	httpClient  *http.Client
	timeout     time.Duration
	throttler   Throttler
	redis       *redis.Client
	queueStart  sync.Once
	botID       string
	shardCount  int
	queueCount  int64
}

// Throttler gates outbound sends. fleet.Limiter implements it with an AIMD
// sliding window; keeping it an interface avoids a package cycle.
type Throttler interface {
	Acquire(ctx context.Context) error
	Penalize(retryAfter time.Duration) time.Duration
}

func NewClient(token string, timeout time.Duration, socks5Proxy string, botID string, throttler Throttler, shardCount int) (*Client, error) {
	httpClient, err := newHTTPClient(timeout, socks5Proxy)
	if err != nil {
		return nil, err
	}
	if shardCount < 1 {
		shardCount = 1
	}
	return &Client{
		token:      token,
		baseURL:    "https://api.telegram.org/bot" + token + "/",
		httpClient: httpClient,
		timeout:    timeout,
		throttler:  throttler,
		botID:      botID,
		shardCount: shardCount,
	}, nil
}

// StartOutboundQueue routes every Bot API call through Redis. Callers still wait
// for the API response, which keeps message-id-dependent operations deterministic.
func (c *Client) StartOutboundQueue(ctx context.Context, redisClient *redis.Client) {
	if redisClient == nil {
		return
	}
	c.queueStart.Do(func() {
		c.redis = redisClient
		if c.shardCount <= 1 {
			go c.runOutboundQueueShard(ctx, -1)
			log.Printf("outbound worker: bot=%s mode=legacy key=outbound:%s", c.botID, c.botID)
		} else {
			for i := 0; i < c.shardCount; i++ {
				go c.runOutboundQueueShard(ctx, i)
			}
			log.Printf("outbound workers: bot=%s shards=%d", c.botID, c.shardCount)
		}
	})
}

// Call sends a request via the current bot's outbound queue.
func (c *Client) Call(ctx context.Context, method string, params map[string]any) (APIResponse, error) {
	return c.callViaQueue(ctx, c.botID, method, params)
}

// CallViaBot sends a request through the outbound queue of another bot.
// This is used for routing messages to users connected to a different shard.
func (c *Client) CallViaBot(ctx context.Context, targetBotID, method string, params map[string]any) (APIResponse, error) {
	return c.callViaQueue(ctx, targetBotID, method, params)
}

func (c *Client) callViaQueue(ctx context.Context, targetBotID, method string, params map[string]any) (APIResponse, error) {
	if c.redis == nil {
		log.Printf("redis-queue: FALLBACK callDirect bot=%s method=%s (redis is nil)", c.botID, method)
		return c.callDirect(ctx, method, params)
	}
	c.queueCount++
	if c.queueCount%100 == 1 {
		log.Printf("redis-queue: bot=%s method=%s via-redis count=%d shard_count=%d", c.botID, method, c.queueCount, c.shardCount)
	}
	queueKey := outboundQueueKeyForBot(targetBotID, c.shardCount, extractChatID(params))
	job, err := newOutboundJob(method, params)
	if err != nil {
		return APIResponse{}, err
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return APIResponse{}, err
	}
	responseKey := outboundResponsePrefix + job.ID

	pipe := c.redis.TxPipeline()
	if method == "answerCallbackQuery" {
		pipe.LPush(ctx, queueKey, raw)
	} else {
		pipe.RPush(ctx, queueKey, raw)
	}
	pipe.Expire(ctx, queueKey, 24*time.Hour)
	if _, err = pipe.Exec(ctx); err != nil {
		return APIResponse{}, fmt.Errorf("enqueue telegram %s: %w", method, err)
	}

	wait := c.timeout + 2*time.Minute
	if wait < 2*time.Minute {
		wait = 2 * time.Minute
	}
	result, err := c.redis.BLPop(ctx, wait, responseKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return APIResponse{}, fmt.Errorf("telegram %s queue response timed out", method)
		}
		return APIResponse{}, err
	}
	if len(result) != 2 {
		return APIResponse{}, fmt.Errorf("telegram %s returned an invalid queue response", method)
	}
	var queued outboundResult
	if err := json.Unmarshal([]byte(result[1]), &queued); err != nil {
		return APIResponse{}, err
	}
	if queued.Error != "" {
		return queued.Response, errors.New(queued.Error)
	}
	if !queued.Response.Ok {
		return queued.Response, fmt.Errorf("telegram %s: %s", method, queued.Response.Description)
	}
	return queued.Response, nil
}

func (c *Client) callDirect(ctx context.Context, method string, params map[string]any) (APIResponse, error) {
	if params == nil {
		params = map[string]any{}
	}

	var body bytes.Buffer
	var contentType string
	if localFiles(params) {
		writer := multipart.NewWriter(&body)
		for key, value := range params {
			switch v := value.(type) {
			case LocalFile:
				if err := writeFilePart(writer, key, v.Path); err != nil {
					return APIResponse{}, err
				}
			default:
				if err := writer.WriteField(key, stringify(value)); err != nil {
					return APIResponse{}, err
				}
			}
		}
		if err := writer.Close(); err != nil {
			return APIResponse{}, err
		}
		contentType = writer.FormDataContentType()
	} else {
		form := url.Values{}
		for key, value := range params {
			form.Set(key, stringify(value))
		}
		body.WriteString(form.Encode())
		contentType = "application/x-www-form-urlencoded"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+method, &body)
	if err != nil {
		return APIResponse{}, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return APIResponse{}, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return APIResponse{}, err
	}
	var apiResp APIResponse
	if err := json.Unmarshal(raw, &apiResp); err != nil {
		return APIResponse{}, fmt.Errorf("telegram %s: %w: %s", method, err, string(raw))
	}
	if !apiResp.Ok {
		if apiResp.ErrorCode == 429 {
			return apiResp, nil
		}
		return apiResp, fmt.Errorf("telegram %s: %s", method, apiResp.Description)
	}
	return apiResp, nil
}

const (
	outboundResponsePrefix = "miogram:telegram:response:"
)

// EnqueueOutbound pushes a fire-and-forget API job onto another bot's outbound
// queue without waiting for a response. Used by the fleet manager for batch
// notifications; the destination instance applies its own adaptive throttling.
// shardCount controls how many parallel queues exist per bot (1 = legacy key).
func EnqueueOutbound(ctx context.Context, rdb *redis.Client, targetBotID, method string, params map[string]any, shardCount int) error {
	if rdb == nil {
		return errors.New("telegram enqueue: nil redis")
	}
	job, err := newOutboundJob(method, params)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	queueKey := outboundQueueKeyForBot(targetBotID, shardCount, extractChatID(params))
	pipe := rdb.TxPipeline()
	pipe.RPush(ctx, queueKey, raw)
	pipe.Expire(ctx, queueKey, 24*time.Hour)
	_, err = pipe.Exec(ctx)
	return err
}

type outboundJob struct {
	ID         string                     `json:"id"`
	Method     string                     `json:"method"`
	Params     map[string]json.RawMessage `json:"params"`
	LocalFiles map[string]string          `json:"local_files,omitempty"`
}

type outboundResult struct {
	Response APIResponse `json:"response"`
	Error    string      `json:"error,omitempty"`
}

func newOutboundJob(method string, params map[string]any) (outboundJob, error) {
	job := outboundJob{ID: randomID(), Method: method, Params: make(map[string]json.RawMessage), LocalFiles: make(map[string]string)}
	for key, value := range params {
		if file, ok := value.(LocalFile); ok {
			job.LocalFiles[key] = file.Path
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return job, fmt.Errorf("encode telegram parameter %s: %w", key, err)
		}
		job.Params[key] = raw
	}
	return job, nil
}

func (j outboundJob) decodedParams() (map[string]any, error) {
	params := make(map[string]any, len(j.Params)+len(j.LocalFiles))
	for key, raw := range j.Params {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		params[key] = value
	}
	for key, path := range j.LocalFiles {
		params[key] = LocalFile{Path: path}
	}
	return params, nil
}

func (c *Client) runOutboundQueueShard(ctx context.Context, shard int) {
	var queueKey string
	if shard < 0 {
		queueKey = "outbound:" + c.botID
	} else {
		queueKey = fmt.Sprintf("outbound:%s:shard:%d", c.botID, shard)
	}
	log.Printf("outbound-worker: started bot=%s shard=%d key=%s", c.botID, shard, queueKey)
	processed := 0
	for {
		item, err := c.redis.BLPop(ctx, time.Second, queueKey).Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		if len(item) != 2 {
			continue
		}
		var job outboundJob
		if json.Unmarshal([]byte(item[1]), &job) != nil || job.ID == "" || job.Method == "" {
			continue
		}
		params, decodeErr := job.decodedParams()
		result := outboundResult{}
		if decodeErr != nil {
			result.Error = decodeErr.Error()
		} else {
			var floodResponses int
			for attempt := 0; attempt < 3; attempt++ {
				// AIMD is the sole rate-control mechanism. A missing
				// throttler is a misconfiguration and must fail loudly
				// rather than send unthrottled.
				if c.throttler == nil {
					result.Error = "AIMD throttler is required but not configured"
					break
				}
				if err := c.throttler.Acquire(ctx); err != nil {
					result.Error = err.Error()
					break
				}
				result.Response, err = c.callDirect(ctx, job.Method, params)
				if err != nil {
					result.Error = err.Error()
					break
				}
				if result.Response.ErrorCode != 429 || result.Response.Parameters.RetryAfter < 1 {
					break
				}
				// Flood response: apply the multiplicative decrease exactly once
				// per distinct flood event, then wait out the server's retry_after.
				floodResponses++
				retryAfter := time.Duration(result.Response.Parameters.RetryAfter) * time.Second
				if floodResponses == 1 {
					retryAfter = c.throttler.Penalize(retryAfter)
				}
				select {
				case <-ctx.Done():
					result.Error = ctx.Err().Error()
				case <-time.After(retryAfter):
				}
			}
			// If the final attempt was a flood (429) and we never broke out with a
			// real error, preserve it so the caller can requeue instead of dropping.
			if floodResponses > 0 && result.Error == "" && result.Response.ErrorCode == 429 {
				result.Error = fmt.Sprintf("flood wait: retry after %d seconds", result.Response.Parameters.RetryAfter)
			}
		}
		raw, _ := json.Marshal(result)
		responseKey := outboundResponsePrefix + job.ID
		pipe := c.redis.TxPipeline()
		pipe.RPush(context.Background(), responseKey, raw)
		pipe.Expire(context.Background(), responseKey, 5*time.Minute)
		_, _ = pipe.Exec(context.Background())
		processed++
		if processed%100 == 1 {
			log.Printf("outbound-worker: bot=%s shard=%d processed=%d method=%s ok=%v", c.botID, shard, processed, job.Method, result.Response.Ok)
		}
	}
}

func randomID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return fmt.Sprintf("%x", raw[:])
}

// fnv32a returns the FNV-1a 32-bit hash of s.
func fnv32a(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// extractChatID pulls the chat_id from params (string or int).
func extractChatID(params map[string]any) string {
	if params == nil {
		return ""
	}
	switch v := params["chat_id"].(type) {
	case string:
		return v
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// outboundQueueKeyForBot returns the Redis key for the outbound queue of
// targetBotID. When shardCount > 1, the chatID hash selects the shard;
// shard -1 or shardCount <=1 uses the legacy key "outbound:<botID>".
func outboundQueueKeyForBot(targetBotID string, shardCount int, chatID string) string {
	if shardCount <= 1 {
		return "outbound:" + targetBotID
	}
	shard := int(fnv32a(chatID)) % shardCount
	if shard < 0 {
		shard += shardCount
	}
	return fmt.Sprintf("outbound:%s:shard:%d", targetBotID, shard)
}

func (c *Client) SentMessage(resp APIResponse) (SentMessage, bool) {
	var msg SentMessage
	if !resp.Ok || len(resp.Result) == 0 {
		return msg, false
	}
	if err := json.Unmarshal(resp.Result, &msg); err != nil {
		return msg, false
	}
	return msg, true
}

func (c *Client) GetMe(ctx context.Context) (User, error) {
	resp, err := c.Call(ctx, "getMe", nil)
	if err != nil {
		return User{}, err
	}
	var user User
	if !resp.Ok {
		return user, errors.New(resp.Description)
	}
	if err := json.Unmarshal(resp.Result, &user); err != nil {
		return User{}, err
	}
	return user, nil
}

func (c *Client) SetWebhook(ctx context.Context, url, secret, certPath string) error {
	params := map[string]any{
		"url":                  url,
		"drop_pending_updates": false,
		"allowed_updates":      `["message","edited_message","callback_query","inline_query"]`,
	}
	if secret != "" {
		params["secret_token"] = secret
	}
	if certPath != "" {
		if _, err := os.Stat(certPath); err == nil {
			params["certificate"] = LocalFile{Path: certPath}
		} else {
			log.Printf("set webhook: certificate file not found: %s", certPath)
		}
	}
	resp, err := c.Call(ctx, "setWebhook", params)
	if err != nil {
		return err
	}
	if !resp.Ok {
		return errors.New(resp.Description)
	}
	return nil
}

func JSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func localFiles(params map[string]any) bool {
	for _, value := range params {
		if _, ok := value.(LocalFile); ok {
			return true
		}
	}
	return false
}

func writeFilePart(writer *multipart.Writer, field, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	part, err := writer.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = io.Copy(part, f)
	return err
}

func stringify(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case json.RawMessage:
		return string(v)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func newHTTPClient(timeout time.Duration, socks5Proxy string) (*http.Client, error) {
	// Transport best practice for the Bot API: a single shared transport with
	// aggressive keep-alive pooling over persistent HTTP/2 connections to
	// api.telegram.org. ForceAttemptHTTP2 is what enables h2 negotiation on
	// TLS; MaxIdleConnsPerHost is raised above the Go default of 2 so bursty
	// worker fan-out reuses sockets instead of re-handshaking.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConns = 256
	transport.MaxIdleConnsPerHost = 64
	transport.MaxConnsPerHost = 0 // unbounded; bounded by pool reuse
	transport.IdleConnTimeout = 90 * time.Second

	socks5Proxy = strings.TrimSpace(socks5Proxy)
	if socks5Proxy == "" {
		return &http.Client{Timeout: timeout, Transport: transport}, nil
	}
	if !strings.Contains(socks5Proxy, "://") {
		socks5Proxy = "socks5://" + socks5Proxy
	}
	proxyURL, err := url.Parse(socks5Proxy)
	if err != nil {
		return nil, fmt.Errorf("parse TELEGRAM_SOCKS5_PROXY: %w", err)
	}
	scheme := strings.ToLower(proxyURL.Scheme)
	if scheme != "socks5" && scheme != "socks5h" {
		return nil, fmt.Errorf("TELEGRAM_SOCKS5_PROXY must use socks5://, got %q", proxyURL.Scheme)
	}
	if proxyURL.Host == "" {
		return nil, errors.New("TELEGRAM_SOCKS5_PROXY host is required")
	}

	var auth *proxy.Auth
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		auth = &proxy.Auth{User: proxyURL.User.Username(), Password: password}
	}
	forward := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, forward)
	if err != nil {
		return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
	}
	transport.Proxy = nil
	transport.DialContext = contextDialer(dialer)
	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

func contextDialer(dialer proxy.Dialer) func(context.Context, string, string) (net.Conn, error) {
	if d, ok := dialer.(proxy.ContextDialer); ok {
		return d.DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		done := make(chan result, 1)
		go func() {
			conn, err := dialer.Dial(network, address)
			done <- result{conn: conn, err: err}
		}()
		select {
		case res := <-done:
			return res.conn, res.err
		case <-ctx.Done():
			go func() {
				res := <-done
				if res.conn != nil {
					_ = res.conn.Close()
				}
			}()
			return nil, ctx.Err()
		}
	}
}
