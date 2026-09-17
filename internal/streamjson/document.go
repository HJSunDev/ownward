package streamjson

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
)

const BufferBytes = 64 * 1024
const nodeBytes = 64

// Document 有界暂存协议负载和节点位置，大负载落盘，遍历数组不构造完整切片。
type Document struct {
	data, index *resourcebudget.File
	ctx         context.Context
	root        Node
	next        int64
	release     func()
	once        sync.Once
	err         error
}

type Node struct {
	d                                                *Document
	position                                         int64
	Kind                                             byte
	Start, End, First, Next, KeyStart, KeyEnd, Count int64
}

func Parse(ctx context.Context, dir string, input io.Reader, budget *resourcebudget.Budget, maxBytes int64) (*Document, error) {
	if budget == nil || maxBytes <= 0 {
		return nil, errors.New("缺少协议工作区或磁盘预算")
	}
	release, err := budget.Acquire(ctx, 4*BufferBytes, false)
	if err != nil {
		return nil, err
	}
	defer release()
	if resourcebudget.DiskFromContext(ctx) == nil {
		ctx = resourcebudget.WithDisk(ctx, resourcebudget.NewDisk(maxBytes))
	}
	d := &Document{ctx: ctx, release: release}
	fail := func(err error) (*Document, error) { d.Close(); return nil, err }
	if err = os.MkdirAll(dir, 0700); err != nil {
		return fail(err)
	}
	d.data, err = resourcebudget.BufferedTempFile(ctx, dir, "rpc-body-", maxBytes, budget)
	if err != nil {
		return fail(err)
	}
	d.index, err = resourcebudget.BufferedTempFile(ctx, dir, "rpc-index-", maxBytes, budget)
	if err != nil {
		return fail(err)
	}
	n, err := io.CopyBuffer(d.data, io.LimitReader(contextReader{ctx, input}, maxBytes+1), make([]byte, BufferBytes))
	if err != nil {
		return fail(err)
	}
	if n > maxBytes {
		return fail(errors.New("协议暂存超过可用磁盘额度"))
	}
	parser := parser{d: d, r: bufio.NewReaderSize(io.NewSectionReader(d.data, 0, n), BufferBytes)}
	d.root, err = parser.value(0)
	if err != nil {
		return fail(err)
	}
	if err = parser.space(); err != nil && err != io.EOF {
		return fail(err)
	}
	if _, err = parser.peek(); err != io.EOF {
		return fail(errors.New("JSON含多余内容"))
	}
	return d, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (d *Document) Root() Node { return d.root }

// RootContext 的读取寿命由材料拥有者控制，不随生成材料的子任务结束。
// 文件的关闭仍只由原 Document 负责。
func (d *Document) RootContext(ctx context.Context) Node {
	view := &Document{data: d.data, index: d.index, ctx: ctx}
	n := d.root
	n.d = view
	return n
}
func (d *Document) Close() error {
	d.once.Do(func() {
		for _, f := range []*resourcebudget.File{d.index, d.data} {
			if f != nil {
				d.err = errors.Join(d.err, f.Close())
			}
		}
		if d.release != nil {
			d.release()
		}
	})
	return d.err
}
func (d *Document) save(n Node) error {
	var b [nodeBytes]byte
	b[0] = n.Kind
	for i, v := range []int64{n.Start, n.End, n.First, n.Next, n.KeyStart, n.KeyEnd, n.Count} {
		binary.LittleEndian.PutUint64(b[8+i*8:], uint64(v))
	}
	_, err := d.index.WriteAt(b[:], n.position)
	return err
}
func (d *Document) node(position int64) (Node, error) {
	var b [nodeBytes]byte
	n := Node{d: d, position: position}
	if _, err := d.index.ReadAt(b[:], position); err != nil {
		return n, err
	}
	n.Kind = b[0]
	fields := []*int64{&n.Start, &n.End, &n.First, &n.Next, &n.KeyStart, &n.KeyEnd, &n.Count}
	for i, v := range fields {
		*v = int64(binary.LittleEndian.Uint64(b[8+i*8:]))
	}
	return n, nil
}

func (n Node) Raw() io.Reader { return io.NewSectionReader(n.d.data, n.Start, n.End-n.Start) }
func (n Node) Copy(w io.Writer) error {
	_, err := io.CopyBuffer(w, contextReader{n.d.ctx, n.Raw()}, make([]byte, BufferBytes))
	return err
}
func (n Node) DecodeSmall(dst any, limit int64) error {
	if n.End-n.Start > limit {
		return errors.New("字段超过内存解码预算")
	}
	return json.NewDecoder(n.Raw()).Decode(dst)
}
func (n Node) String(limit int64) (string, error) {
	if n.Kind != '"' {
		return "", errors.New("字段必须为字符串")
	}
	var out string
	err := n.DecodeSmall(&out, limit)
	return out, err
}
func (n Node) Key() (string, error) {
	if n.KeyEnd <= n.KeyStart {
		return "", errors.New("节点没有字段名")
	}
	var key string
	err := json.NewDecoder(io.NewSectionReader(n.d.data, n.KeyStart, n.KeyEnd-n.KeyStart)).Decode(&key)
	return key, err
}

type Cursor struct {
	d    *Document
	next int64
}

func (n Node) Children() Cursor { return Cursor{n.d, n.First} }
func (c *Cursor) Next() (Node, error) {
	if c.next < 0 {
		return Node{}, io.EOF
	}
	n, err := c.d.node(c.next)
	if err != nil {
		return n, err
	}
	c.next = n.Next
	return n, nil
}
func (n Node) Field(key string) (Node, bool, error) {
	if n.Kind != '{' {
		return Node{}, false, errors.New("字段所属值不是对象")
	}
	c := n.Children()
	for {
		child, err := c.Next()
		if err == io.EOF {
			return Node{}, false, nil
		}
		if err != nil {
			return Node{}, false, err
		}
		name, err := child.Key()
		if err != nil {
			return Node{}, false, err
		}
		if name == key {
			return child, true, nil
		}
	}
}

type parser struct {
	d          *Document
	r          *bufio.Reader
	offset     int64
	activeKeys int
}

func (p *parser) peek() (byte, error) {
	b, err := p.r.Peek(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}
func (p *parser) take() (byte, error) {
	if err := p.d.ctx.Err(); err != nil {
		return 0, err
	}
	b, err := p.r.ReadByte()
	if err == nil {
		p.offset++
	}
	return b, err
}
func (p *parser) space() error {
	for {
		b, err := p.peek()
		if err != nil {
			return err
		}
		if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
			return nil
		}
		if _, err = p.take(); err != nil {
			return err
		}
	}
}
func (p *parser) value(depth int) (Node, error) {
	if depth > 128 {
		return Node{}, errors.New("JSON层次超过工具结构边界")
	}
	if err := p.space(); err != nil {
		return Node{}, err
	}
	b, err := p.peek()
	if err != nil {
		return Node{}, err
	}
	n := Node{d: p.d, position: p.d.next, Kind: b, Start: p.offset, First: -1, Next: -1}
	p.d.next += nodeBytes
	switch b {
	case '"':
		err = p.string()
	case '{', '[':
		_, err = p.take()
		if err != nil {
			return n, err
		}
		end := byte('}')
		if b == '[' {
			end = ']'
		}
		if err = p.space(); err != nil {
			return n, err
		}
		var last *Node
		// 工具对象的字段数有限；数组项与大字段仅记录磁盘位置。
		keys := map[string]bool{}
		keyBytes := 0
		defer func() { p.activeKeys -= keyBytes }()
		for {
			peek, e := p.peek()
			if e != nil {
				return n, e
			}
			if peek == end {
				_, err = p.take()
				break
			}
			ks, ke := int64(0), int64(0)
			if b == '{' {
				ks = p.offset
				if err = p.string(); err != nil {
					return n, err
				}
				ke = p.offset
				if ke-ks > 1024 {
					return n, errors.New("字段名不属于工具结构")
				}
				var key string
				if err = json.NewDecoder(io.NewSectionReader(p.d.data, ks, ke-ks)).Decode(&key); err != nil {
					return n, err
				}
				if keys[key] {
					return n, fmt.Errorf("重复JSON字段: %s", key)
				}
				keys[key] = true
				cost := len(key) + 256
				keyBytes += cost
				p.activeKeys += cost
				if p.activeKeys > BufferBytes {
					return n, errors.New("对象字段超过工具结构预算")
				}
				if err = p.space(); err != nil {
					return n, err
				}
				v, e := p.take()
				if e != nil || v != ':' {
					return n, errors.New("JSON字段缺少冒号")
				}
			}
			child, e := p.value(depth + 1)
			if e != nil {
				return n, e
			}
			child.KeyStart, child.KeyEnd = ks, ke
			if err = p.d.save(child); err != nil {
				return n, err
			}
			if last == nil {
				n.First = child.position
			} else {
				last.Next = child.position
				if err = p.d.save(*last); err != nil {
					return n, err
				}
			}
			last = &child
			n.Count++
			if err = p.space(); err != nil {
				return n, err
			}
			v, e := p.take()
			if e != nil {
				return n, e
			}
			if v == end {
				break
			}
			if v != ',' {
				return n, errors.New("JSON集合缺少分隔符")
			}
			if err = p.space(); err != nil {
				return n, err
			}
			v, e = p.peek()
			if e != nil {
				return n, e
			}
			if v == end {
				return n, errors.New("JSON尾随逗号")
			}
		}
	default:
		var token []byte
		for {
			v, e := p.peek()
			if e == io.EOF {
				break
			}
			if e != nil {
				return n, e
			}
			if v == ',' || v == ']' || v == '}' || v == ' ' || v == '\n' || v == '\r' || v == '\t' {
				break
			}
			v, e = p.take()
			if e != nil {
				return n, e
			}
			token = append(token, v)
			if len(token) > 1024 {
				return n, errors.New("JSON标量过长")
			}
		}
		if len(token) == 0 || !json.Valid(token) {
			return n, errors.New("无效JSON标量")
		}
		if b != 'n' && b != 't' && b != 'f' {
			if _, err = strconv.ParseFloat(string(token), 64); err != nil {
				return n, err
			}
			n.Kind = '0'
		}
	}
	if err != nil {
		return n, err
	}
	n.End = p.offset
	return n, p.d.save(n)
}
func (p *parser) string() error {
	v, err := p.take()
	if err != nil || v != '"' {
		return errors.New("JSON字段必须为字符串")
	}
	for {
		v, err = p.take()
		if err != nil {
			return err
		}
		if v == '"' {
			return nil
		}
		if v < 32 {
			return errors.New("字符串包含控制字符")
		}
		if v != '\\' {
			continue
		}
		v, err = p.take()
		if err != nil {
			return err
		}
		switch v {
		case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		case 'u':
			for i := 0; i < 4; i++ {
				v, err = p.take()
				if err != nil {
					return err
				}
				if !((v >= '0' && v <= '9') || (v >= 'a' && v <= 'f') || (v >= 'A' && v <= 'F')) {
					return errors.New("无效Unicode转义")
				}
			}
		default:
			return errors.New("无效字符串转义")
		}
	}
}
