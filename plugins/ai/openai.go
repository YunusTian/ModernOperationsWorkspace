package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mow/mow/sdk"
)

type openAIOptions struct {
	BaseURL        string            `json:"base_url"`
	APIKeyEnv      string            `json:"api_key_env"`
	DefaultModel   string            `json:"default_model"`
	Models         []string          `json:"models"`
	Headers        map[string]string `json:"headers"`
	TimeoutSeconds int               `json:"timeout_seconds"`

	// Retry 参数：仅对 429 / 5xx 与 dial 阶段的可重试网络错误生效。
	// 取值 0 → 使用默认；负值被 clamp 到默认。
	RetryMaxAttempts   int `json:"retry_max_attempts"`     // 默认 3；1 表示不重试
	RetryBaseBackoffMS int `json:"retry_base_backoff_ms"`  // 默认 500ms
	RetryMaxBackoffMS  int `json:"retry_max_backoff_ms"`   // 默认 5000ms
}

// retryPolicy 是 provider 内部使用的重试参数。
type retryPolicy struct {
	MaxAttempts int
	Base        time.Duration
	Max         time.Duration
}

// defaultRetryPolicy 是 v0.4.1 默认策略：3 次总尝试，500ms → 1s → 2s（上限 5s）。
var defaultRetryPolicy = retryPolicy{MaxAttempts: 3, Base: 500 * time.Millisecond, Max: 5 * time.Second}

type openAIProvider struct {
	name         string
	baseURL      string
	apiKey       string
	defaultModel string
	models       []string
	headers      map[string]string
	client       *http.Client
	retry        retryPolicy
	// sleep 支持在测试中替换，避免真的等待。
	sleep func(context.Context, time.Duration) error
}

func newOpenAIProvider(pc providerSettings) (*openAIProvider, error) {
	var o openAIOptions
	if len(pc.Options) != 0 {
		if err := json.Unmarshal(pc.Options, &o); err != nil {
			return nil, fmt.Errorf("decode options: %w", err)
		}
	}
	if o.BaseURL == "" {
		o.BaseURL = "https://api.openai.com/v1"
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("base_url must be an absolute http(s) URL")
	}
	keyEnv := o.APIKeyEnv
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	key := os.Getenv(keyEnv)
	if key == "" {
		return nil, fmt.Errorf("API key environment variable %s is not set", keyEnv)
	}
	name := pc.Name
	if name == "" {
		name = pc.Kind
	}
	timeout := time.Duration(o.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &openAIProvider{
		name: name, baseURL: strings.TrimRight(o.BaseURL, "/"), apiKey: key,
		defaultModel: o.DefaultModel, models: append([]string(nil), o.Models...),
		headers: o.Headers, client: &http.Client{Timeout: timeout},
		retry: buildRetryPolicy(o), sleep: sleepCtx,
	}, nil
}

// buildRetryPolicy 从 options 拼出重试参数；空 / 负值走默认。
func buildRetryPolicy(o openAIOptions) retryPolicy {
	p := defaultRetryPolicy
	if o.RetryMaxAttempts > 0 {
		p.MaxAttempts = o.RetryMaxAttempts
	}
	if o.RetryBaseBackoffMS > 0 {
		p.Base = time.Duration(o.RetryBaseBackoffMS) * time.Millisecond
	}
	if o.RetryMaxBackoffMS > 0 {
		p.Max = time.Duration(o.RetryMaxBackoffMS) * time.Millisecond
	}
	if p.Max < p.Base {
		p.Max = p.Base
	}
	return p
}

// sleepCtx 是 time.Sleep 的可取消版本；ctx.Done 立即返回。
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoffFor 计算第 attempt 次（1-based）失败后的退避时长：Base * 2^(attempt-1)，
// 上限 Max。
func (p *openAIProvider) backoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := p.retry.Base << (attempt - 1)
	if d <= 0 || d > p.retry.Max {
		d = p.retry.Max
	}
	return d
}

func (p *openAIProvider) Name() string { return p.name }
func (p *openAIProvider) Capabilities() sdk.ProviderCapabilities {
	return sdk.ProviderCapabilities{Chat: true, ChatStream: true, ToolCalls: true, Models: append([]string(nil), p.models...)}
}

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools,omitempty"`
	Temperature *float32        `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}
type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}
type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}
type openAIToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}
type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
type openAIResponse struct {
	Choices []struct {
		Message      openAIMessage `json:"message"`
		Delta        openAIMessage `json:"delta"`
		FinishReason string        `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

func (p *openAIProvider) request(req sdk.ChatRequest, stream bool) (openAIRequest, error) {
	model := req.Model
	if model == "" {
		model = p.defaultModel
	}
	if model == "" {
		return openAIRequest{}, sdk.NewError("AI_MODEL_REQUIRED", "model is required", nil)
	}
	r := openAIRequest{Model: model, MaxTokens: req.MaxTokens, Stream: stream}
	if req.Temp != 0 {
		t := req.Temp
		r.Temperature = &t
	}
	for _, m := range req.Messages {
		om := openAIMessage{Role: m.Role, Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			om.ToolCalls = append(om.ToolCalls, encodeToolCall(tc))
		}
		r.Messages = append(r.Messages, om)
	}
	for _, t := range req.Tools {
		schema := t.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		r.Tools = append(r.Tools, openAITool{Type: "function", Function: openAIFunction{Name: t.Name, Description: t.Description, Parameters: schema}})
	}
	return r, nil
}

func encodeToolCall(tc sdk.ToolCall) openAIToolCall {
	var out openAIToolCall
	out.ID = tc.ID
	out.Type = "function"
	out.Function.Name = tc.Name
	out.Function.Arguments = string(tc.Args)
	return out
}

func decodeMessage(m openAIMessage) sdk.ChatMessage {
	out := sdk.ChatMessage{Role: m.Role, Content: m.Content, Name: m.Name, ToolCallID: m.ToolCallID}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, sdk.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: json.RawMessage(tc.Function.Arguments)})
	}
	return out
}

func (p *openAIProvider) do(ctx context.Context, payload openAIRequest) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	max := p.retry.MaxAttempts
	if max < 1 {
		max = 1
	}
	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		resp, err := p.doOnce(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == max {
			return nil, err
		}
		if sleepErr := p.sleep(ctx, p.backoffFor(attempt)); sleepErr != nil {
			// ctx 取消 / 超时 → 立即返回，退化为 canceled/timeout 语义。
			if errors.Is(sleepErr, context.Canceled) {
				return nil, sdk.ErrCanceled
			}
			return nil, sdk.ErrTimeout
		}
	}
	return nil, lastErr
}

// isRetryable 判断一个 do 层错误是否值得再试。
//   - *sdk.Error.Retryable = true → 允许
//   - sdk.ErrCanceled / sdk.ErrTimeout → 一律不重试（用户显式取消 or 已耗尽整体超时）
func isRetryable(err error) bool {
	if errors.Is(err, sdk.ErrCanceled) || errors.Is(err, sdk.ErrTimeout) {
		return false
	}
	var se *sdk.Error
	if errors.As(err, &se) {
		return se.Retryable
	}
	return false
}

// doOnce 发起一次 HTTP 请求；调用方需保证 body 可被多次读取（此处复制自 []byte）。
func (p *openAIProvider) doOnce(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, sdk.ErrCanceled
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, sdk.ErrTimeout
		}
		return nil, sdk.NewError("AI_PROVIDER_UNAVAILABLE", "provider request failed", err).WithRetryable(true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, mapOpenAIHTTPError(resp)
	}
	return resp, nil
}

func mapOpenAIHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	msg := strings.TrimSpace(envelope.Error.Message)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	code, retry := "AI_PROVIDER_ERROR", false
	switch resp.StatusCode {
	case 401:
		code = "AI_AUTH_FAILED"
	case 403:
		code = "AI_ACCESS_DENIED"
	case 429:
		code = "AI_RATE_LIMITED"
		retry = true
	case 500, 502, 503, 504:
		code = "AI_PROVIDER_UNAVAILABLE"
		retry = true
	}
	return sdk.NewError(code, msg, nil).WithRetryable(retry).WithDetails(map[string]any{"status": resp.StatusCode})
}

func (p *openAIProvider) Chat(ctx context.Context, req sdk.ChatRequest) (*sdk.ChatResponse, error) {
	payload, err := p.request(req, false)
	if err != nil {
		return nil, err
	}
	resp, err := p.do(ctx, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw openAIResponse
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&raw); err != nil {
		return nil, sdk.NewError("AI_PROVIDER_PROTOCOL", "decode response failed", err)
	}
	if len(raw.Choices) == 0 {
		return nil, sdk.NewError("AI_PROVIDER_PROTOCOL", "response has no choices", nil)
	}
	c := raw.Choices[0]
	return &sdk.ChatResponse{Message: decodeMessage(c.Message), Usage: sdk.ChatUsage{PromptTokens: raw.Usage.PromptTokens, CompletionTokens: raw.Usage.CompletionTokens, TotalTokens: raw.Usage.TotalTokens}, Finish: c.FinishReason}, nil
}

func (p *openAIProvider) ChatStream(ctx context.Context, req sdk.ChatRequest, sink sdk.ChatStreamSink) error {
	payload, err := p.request(req, true)
	if err != nil {
		return err
	}
	resp, err := p.do(ctx, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	var content, finish string
	calls := map[int]*openAIToolCall{}
	usage := sdk.ChatUsage{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk openAIResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return sdk.NewError("AI_PROVIDER_PROTOCOL", "decode stream chunk failed", err)
		}
		usage = sdk.ChatUsage{PromptTokens: chunk.Usage.PromptTokens, CompletionTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
		if len(chunk.Choices) == 0 {
			continue
		}
		c := chunk.Choices[0]
		if c.Delta.Content != "" {
			content += c.Delta.Content
			if err := sink.OnDelta(c.Delta.Content); err != nil {
				return err
			}
		}
		for _, part := range c.Delta.ToolCalls {
			acc := calls[part.Index]
			if acc == nil {
				acc = &openAIToolCall{Index: part.Index}
				calls[part.Index] = acc
			}
			if part.ID != "" {
				acc.ID = part.ID
			}
			if part.Function.Name != "" {
				acc.Function.Name += part.Function.Name
			}
			acc.Function.Arguments += part.Function.Arguments
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
	}
	if err := scanner.Err(); err != nil {
		return sdk.NewError("AI_PROVIDER_PROTOCOL", "read stream failed", err).WithRetryable(true)
	}
	msg := sdk.ChatMessage{Role: sdk.RoleAssistant, Content: content}
	for i := 0; i < len(calls); i++ {
		tc := calls[i]
		if tc == nil {
			continue
		}
		decoded := sdk.ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: json.RawMessage(tc.Function.Arguments)}
		msg.ToolCalls = append(msg.ToolCalls, decoded)
		if err := sink.OnToolCall(decoded); err != nil {
			return err
		}
	}
	if finish == "" {
		finish = sdk.FinishStop
	}
	return sink.OnFinish(sdk.ChatResponse{Message: msg, Usage: usage, Finish: finish})
}
