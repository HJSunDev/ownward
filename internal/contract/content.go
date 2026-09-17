package contract

import (
	"context"
	"io"
)

// ContentSource 提供同一份不可变正文的重复读取，不要求调用方持有整篇内容。
type ContentSource interface {
	Open(context.Context) (io.ReadCloser, error)
}

// ContentRange 使用原始 UTF-8 字节偏移；文本定位到字节位置的转换由取证层负责。
type ContentRange struct {
	Offset int64
	Length int64
}

type StreamRequest struct {
	Operation string
	Arguments ContentSource
}

// StreamResult 的正文已经脱离数据库事务，Close 回收本次交付材料。
type StreamResult struct {
	Value ContentSource
	Check func(context.Context) error
	Close func() error
	// Retain binds a transport copy to the same delivery authority. The returned
	// release closes that copy; invalidation closes all retained copies.
	Retain func(func() error) (func() error, error)
}

type StreamingProduct interface {
	ExecuteStream(context.Context, StreamRequest) (*StreamResult, error)
}

type authenticationKey struct{}

func WithAuthenticationDigest(ctx context.Context, digest string) context.Context {
	return context.WithValue(ctx, authenticationKey{}, digest)
}
func AuthenticationDigest(ctx context.Context) string {
	value, _ := ctx.Value(authenticationKey{}).(string)
	return value
}
