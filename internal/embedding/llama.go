package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type Managed struct {
	bundle Bundle
	client *http.Client

	mu        sync.Mutex
	requestMu sync.Mutex
	command   *exec.Cmd
	done      chan error
	lifetime  io.Closer
	port      int
	closed    bool
	logs      bytes.Buffer
}

func OpenManaged(root string) (*Managed, error) {
	bundle, err := LoadBundle(root)
	if err != nil {
		return nil, err
	}
	return OpenManagedBundle(bundle)
}

// OpenManagedBundle 复用 LoadBundle 已经完整校验的能力包。
// 未导出的校验标记既阻止调用方绕过完整性验证，也避免对同一制品重复计算摘要。
func OpenManagedBundle(bundle Bundle) (*Managed, error) {
	if !bundle.verified {
		return nil, errors.New("本地向量能力包尚未完成完整性校验")
	}
	return &Managed{
		bundle: bundle,
		client: &http.Client{Transport: &http.Transport{Proxy: nil}},
	}, nil
}

func (m *Managed) Name() string { return m.bundle.Manifest.Capability }
func (m *Managed) Space() Space {
	return Space{ID: m.bundle.Manifest.Space.ID, Dimensions: m.bundle.Manifest.Space.Dimensions}
}

func (m *Managed) EmbedDocuments(ctx context.Context, values []string) ([][]float32, error) {
	inputs := make([]string, len(values))
	for index, value := range values {
		inputs[index] = m.bundle.Manifest.Space.DocumentPrefix + value
	}
	return m.embed(ctx, inputs)
}

func (m *Managed) EmbedQuery(ctx context.Context, value string) ([]float32, error) {
	vectors, err := m.embed(ctx, []string{m.bundle.Manifest.Space.QueryPrefix + value})
	if err != nil {
		return nil, err
	}
	if len(vectors) != 1 {
		return nil, errors.New("本地向量运行时返回数量无效")
	}
	return vectors[0], nil
}

func (m *Managed) embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if len(inputs) > 32 {
		return nil, errors.New("单次向量请求超过三十二条")
	}
	m.requestMu.Lock()
	defer m.requestMu.Unlock()
	if err := m.ensureRunning(ctx); err != nil {
		return nil, err
	}
	body := map[string]any{"input": inputs, "model": "embeddinggemma"}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://127.0.0.1:"+strconv.Itoa(m.port)+"/v1/embeddings", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := m.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("调用本地向量运行时: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("本地向量运行时返回 %s: %s", response.Status, bytes.TrimSpace(message))
	}
	var payload struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024*1024)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("解析本地向量结果: %w", err)
	}
	if len(payload.Data) != len(inputs) {
		return nil, errors.New("本地向量运行时返回数量不匹配")
	}
	sort.Slice(payload.Data, func(left, right int) bool { return payload.Data[left].Index < payload.Data[right].Index })
	result := make([][]float32, len(inputs))
	for position, item := range payload.Data {
		if item.Index != position || len(item.Embedding) < m.bundle.Manifest.Space.SourceDimensions {
			return nil, errors.New("本地向量运行时返回维度或顺序无效")
		}
		vector := append([]float32(nil), item.Embedding[:m.bundle.Manifest.Space.Dimensions]...)
		for _, component := range vector {
			if math.IsNaN(float64(component)) || math.IsInf(float64(component), 0) {
				return nil, errors.New("本地向量运行时返回非有限数值")
			}
		}
		normalize(vector)
		if vectorNorm(vector) == 0 {
			return nil, errors.New("本地向量运行时返回零向量")
		}
		result[position] = vector
	}
	return result, nil
}

func (m *Managed) ensureRunning(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errors.New("本地向量能力已关闭")
	}
	if m.command != nil {
		select {
		case exitErr := <-m.done:
			m.closeLifetimeLocked()
			m.command = nil
			m.done = nil
			if exitErr != nil {
				m.logs.WriteString("; previous exit: " + exitErr.Error())
			}
		default:
			return nil
		}
	}
	port, err := availablePort()
	if err != nil {
		return err
	}
	arguments := []string{
		"-m", m.bundle.ModelPath,
		"--embeddings", "--pooling", "mean", "--embd-normalize", "2",
		"--host", "127.0.0.1", "--port", strconv.Itoa(port),
		"--threads", "2", "--threads-batch", "2", "--parallel", "1",
		"--ctx-size", "512", "--batch-size", "512", "--ubatch-size", "512",
		"--no-warmup", "--no-webui", "--log-disable",
	}
	command := exec.Command(m.bundle.RuntimePath, arguments...)
	command.Dir = filepath.Dir(m.bundle.RuntimePath)
	hideProcessWindow(command)
	m.logs.Reset()
	command.Stdout = &m.logs
	command.Stderr = &m.logs
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动本地向量运行时: %w", err)
	}
	lifetime, err := attachProcessLifetime(command)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("绑定本地向量运行时生命周期: %w", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	m.command = command
	m.done = done
	m.lifetime = lifetime
	m.port = port
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			m.stopLocked()
			return ctx.Err()
		case exitErr := <-done:
			m.closeLifetimeLocked()
			m.command = nil
			m.done = nil
			return fmt.Errorf("本地向量运行时提前退出: %v: %s", exitErr, m.logs.String())
		default:
		}
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+"/health", nil)
		response, requestErr := m.client.Do(request)
		if requestErr == nil {
			var health struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&health)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && health.Status == "ok" {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	m.stopLocked()
	return fmt.Errorf("本地向量运行时启动超时: %s", m.logs.String())
}

func (m *Managed) Close() error {
	m.requestMu.Lock()
	defer m.requestMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.stopLocked()
}

func (m *Managed) stopLocked() error {
	if m.command == nil {
		return nil
	}
	command := m.command
	done := m.done
	lifetime := m.lifetime
	m.command = nil
	m.done = nil
	m.lifetime = nil
	if lifetime != nil {
		_ = lifetime.Close()
	}
	if command.Process != nil {
		_ = command.Process.Kill()
	}
	if done != nil {
		select {
		case err := <-done:
			if err != nil && command.ProcessState == nil {
				return err
			}
		case <-time.After(10 * time.Second):
			return errors.New("本地向量运行时未能及时退出")
		}
	}
	return nil
}

func (m *Managed) closeLifetimeLocked() {
	if m.lifetime != nil {
		_ = m.lifetime.Close()
		m.lifetime = nil
	}
}

func availablePort() (int, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("分配本地向量端口: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

func vectorNorm(vector []float32) float64 {
	value := float64(0)
	for _, component := range vector {
		value += float64(component * component)
	}
	return math.Sqrt(value)
}
