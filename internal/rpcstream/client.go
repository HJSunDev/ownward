package rpcstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

// RoundTripper 在HTTP边界还原原协议。认证头由现有认证传输层附加。
type RoundTripper struct {
	Scope *Scope
	Next  http.RoundTripper
}

func (t *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	next := t.Next
	if next == nil {
		next = http.DefaultTransport
	}
	if req.Method != http.MethodPost || req.Body == nil {
		return next.RoundTrip(req)
	}
	raw, err := io.ReadAll(io.LimitReader(req.Body, EnvelopeBytes+1))
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > EnvelopeBytes {
		return nil, errors.New("客户端SDK输入超过信封预算")
	}
	var envelope struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	request := req.Clone(req.Context())
	request.Body = io.NopCloser(bytes.NewReader(raw))
	request.ContentLength = int64(len(raw))
	request.GetBody = nil
	if envelope.Method != "tools/call" || !StorageTool(envelope.Params.Name) {
		return next.RoundTrip(request)
	}
	call, err := t.Scope.Resolve(envelope.Params.Name, envelope.Params.Arguments)
	if err != nil {
		return nil, err
	}
	call.mu.Lock()
	defer call.mu.Unlock()
	if call.closed {
		return nil, errors.New("调用已经关闭")
	}
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	stop := context.AfterFunc(call.Context(), cancel)
	defer stop()
	req = req.WithContext(resourcebudget.WithDisk(ctx, t.Scope.Disk))
	request = request.WithContext(req.Context())
	doc, err := streamjson.Parse(req.Context(), t.Scope.Dir, bytes.NewReader(raw), t.Scope.Budget, t.Scope.DiskBytes)
	if err != nil {
		return nil, err
	}
	defer doc.Close()
	params, _, _ := doc.Root().Field("params")
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() {
		e := doc.Root().Object(w, map[string]func(io.Writer) error{"params": func(out io.Writer) error {
			return params.Object(out, map[string]func(io.Writer) error{"arguments": call.Arguments.Copy})
		}})
		w.CloseWithError(e)
		done <- e
	}()
	request.Body = r
	request.ContentLength = -1
	request.Header.Del("Content-Length")
	response, err := next.RoundTrip(request)
	r.Close()
	writeErr := <-done
	if err != nil {
		return nil, err
	}
	if writeErr != nil {
		response.Body.Close()
		return nil, writeErr
	}
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, EnvelopeBytes+1))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(body) > EnvelopeBytes {
			return nil, errors.New("HTTP错误信息超过信封预算")
		}
		response.Body = io.NopCloser(bytes.NewReader(body))
		return response, nil
	}
	result, err := streamjson.Parse(req.Context(), t.Scope.Dir, response.Body, t.Scope.Budget, t.Scope.DiskBytes)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			result.Close()
		}
	}()
	id, ok, err := result.Root().Field("id")
	if err != nil || !ok {
		return nil, errors.New("HTTP结果缺少调用身份")
	}
	var actualID json.RawMessage
	if err = id.DecodeSmall(&actualID, 1024); err != nil {
		return nil, err
	}
	if !sameRPCIdentity(actualID, envelope.ID) {
		return nil, errors.New("HTTP结果不属于当前调用")
	}
	value, ok, err := result.RootContext(call.ctx).Field("result")
	if err != nil {
		return nil, err
	}
	var compact limitBuffer
	if !ok {
		if err = result.Root().Copy(&compact); err != nil {
			return nil, err
		}
	} else {
		structured, present, e := value.Field("structuredContent")
		if e != nil {
			return nil, e
		}
		if !present {
			if err = result.Root().Copy(&compact); err != nil {
				return nil, err
			}
		} else {
			call.output = result
			call.outputNode = structured
			keep = true
			err = result.Root().Object(&compact, map[string]func(io.Writer) error{"result": func(out io.Writer) error {
				return value.Object(out, map[string]func(io.Writer) error{"structuredContent": literal(map[string]string{outputField: call.token}), "content": literal([]map[string]string{{"type": "text", "text": call.token}})})
			}})
			if err != nil {
				return nil, err
			}
		}
	}
	response.Body = io.NopCloser(bytes.NewReader(compact.Bytes()))
	response.ContentLength = int64(compact.Len())
	response.Header.Del("Content-Length")
	return response, nil
}
