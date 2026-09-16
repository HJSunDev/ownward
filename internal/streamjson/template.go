package streamjson

import (
	"context"
	"encoding/json"
	"io"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

// Build 将生成结果直接落盘，调用方不构造完整的 JSON 字符串。
func Build(ctx context.Context, dir string, budget *resourcebudget.Budget, maxBytes int64, write func(io.Writer) error) (*Document, error) {
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() { err := write(w); _ = w.CloseWithError(err); done <- err }()
	d, err := Parse(ctx, dir, r, budget, maxBytes)
	_ = r.CloseWithError(err)
	writerErr := <-done
	if err != nil {
		return nil, err
	}
	if writerErr != nil {
		d.Close()
		return nil, writerErr
	}
	return d, nil
}

// Object 只遍历一层字段；替换值直接写出，未变字段按原位置复制。
func (n Node) Object(w io.Writer, replace map[string]func(io.Writer) error) error {
	if _, err := io.WriteString(w, "{"); err != nil {
		return err
	}
	c := n.Children()
	first := true
	for {
		child, err := c.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		key, err := child.Key()
		if err != nil {
			return err
		}
		if !first {
			if _, err = io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		b, _ := json.Marshal(key)
		if _, err = w.Write(b); err != nil {
			return err
		}
		if _, err = io.WriteString(w, ":"); err != nil {
			return err
		}
		if fn, ok := replace[key]; ok {
			err = fn(w)
		} else {
			err = child.Copy(w)
		}
		if err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "}")
	return err
}
