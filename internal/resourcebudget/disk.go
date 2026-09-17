package resourcebudget

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
)

// Disk 为同一服务的暂存文件共享额度；空间不足在写入前失败，不等待自己释放空间。
type Disk struct {
	mu          sync.Mutex
	limit, used int64
}

func NewDisk(limit int64) *Disk { return &Disk{limit: limit} }

func CheckFree(path string, required uint64) error {
	available, e := freeBytes(path)
	if e != nil {
		return e
	}
	if available < required {
		return errors.New("磁盘可用空间不足以保留原资料并建立新存储")
	}
	return nil
}

type diskKey struct{}

func WithDisk(ctx context.Context, d *Disk) context.Context {
	return context.WithValue(ctx, diskKey{}, d)
}
func DiskFromContext(ctx context.Context) *Disk { d, _ := ctx.Value(diskKey{}).(*Disk); return d }
func (d *Disk) Used() int64                     { d.mu.Lock(); defer d.mu.Unlock(); return d.used }
func (d *Disk) reserve(n int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n < 0 || n > d.limit-d.used {
		return errors.New("暂存空间额度不足")
	}
	d.used += n
	return nil
}

// File 同时计入正文、节点索引及定位工作文件，关闭即释放额度和文件。
type File struct {
	*os.File
	disk          *Disk
	size, checked int64
	buffer        []byte
	offset        int64
	dir, prefix   string
	release       func()
	closed        bool
	once          sync.Once
	err           error
}

// BufferedTempFile keeps at most 8 KiB of temporary data in an admitted buffer.
// Larger data spills to the same quota-accounted file; durable data never uses it.
func BufferedTempFile(ctx context.Context, dir, prefix string, fallback int64, budget *Budget) (*File, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if budget == nil {
		return TempFile(ctx, dir, prefix, fallback)
	}
	release, ok := budget.TryAcquire(8192)
	if !ok {
		return TempFile(ctx, dir, prefix, fallback)
	}
	d := DiskFromContext(ctx)
	if d == nil {
		d = NewDisk(fallback)
	}
	return &File{disk: d, buffer: make([]byte, 0, 8192), dir: dir, prefix: prefix, release: release}, nil
}

func (f *File) spill() error {
	if f.File != nil {
		return nil
	}
	v, e := os.CreateTemp(f.dir, f.prefix)
	if e != nil {
		return e
	}
	fail := func(e error) error { v.Close(); os.Remove(v.Name()); return e }
	if e = CheckFree(v.Name(), uint64(f.size+9*MiB)); e != nil {
		return fail(e)
	}
	if _, e = v.Write(f.buffer); e != nil {
		return fail(e)
	}
	if _, e = v.Seek(f.offset, io.SeekStart); e != nil {
		return fail(e)
	}
	f.File = v
	f.checked = f.size + MiB
	f.buffer = nil
	f.release()
	f.release = nil
	return nil
}

func (f *File) ReadAt(p []byte, offset int64) (int, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.File != nil {
		return f.File.ReadAt(p, offset)
	}
	if offset < 0 {
		return 0, errors.New("negative read offset")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if offset >= int64(len(f.buffer)) {
		return 0, io.EOF
	}
	n := copy(p, f.buffer[offset:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
func (f *File) Read(p []byte) (int, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.File != nil {
		return f.File.Read(p)
	}
	n, e := f.ReadAt(p, f.offset)
	f.offset += int64(n)
	return n, e
}
func (f *File) Seek(offset int64, whence int) (int64, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	if f.File != nil {
		return f.File.Seek(offset, whence)
	}
	base := int64(0)
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = f.offset
	case io.SeekEnd:
		base = int64(len(f.buffer))
	default:
		return 0, errors.New("invalid seek")
	}
	pos := base + offset
	if pos < 0 || (offset > 0 && pos < base) {
		return 0, errors.New("invalid seek offset")
	}
	f.offset = pos
	return pos, nil
}

// 覆盖 os.File 的快速复制接口，防止 io.Copy 绕过逐块额度检查。
func (f *File) ReadFrom(r io.Reader) (int64, error) {
	return io.CopyBuffer(struct{ io.Writer }{f}, r, make([]byte, 64*1024))
}
func (f *File) WriteTo(w io.Writer) (int64, error) {
	return io.CopyBuffer(w, struct{ io.Reader }{f}, make([]byte, 64*1024))
}
func (f *File) WriteString(s string) (int, error) { return f.Write([]byte(s)) }

func TempFile(ctx context.Context, dir, prefix string, fallback int64) (*File, error) {
	d := DiskFromContext(ctx)
	if d == nil {
		d = NewDisk(fallback)
	}
	f, err := os.CreateTemp(dir, prefix)
	if err != nil {
		return nil, err
	}
	return &File{File: f, disk: d}, nil
}
func (f *File) grow(end int64) error {
	if end < 0 {
		return errors.New("invalid file size")
	}
	if end <= f.size {
		return nil
	}
	n := end - f.size
	// 已保留额度还需对应磁盘余量；为当前写入之外保留8 MiB。
	if f.File != nil && end > f.checked {
		if free, err := freeBytes(f.Name()); err != nil {
			return err
		} else if free < uint64(n+9*MiB) {
			return errors.New("磁盘可用空间不足")
		}
		f.checked = end + MiB
	}
	if err := f.disk.reserve(n); err != nil {
		return err
	}
	f.size = end
	return nil
}
func (f *File) Write(p []byte) (int, error) {
	offset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if err = f.grow(offset + int64(len(p))); err != nil {
		return 0, err
	}
	if f.File == nil {
		if offset+int64(len(p)) <= int64(cap(f.buffer)) {
			end := int(offset) + len(p)
			if end > len(f.buffer) {
				f.buffer = f.buffer[:end]
			}
			n := copy(f.buffer[offset:], p)
			f.offset += int64(n)
			return n, nil
		}
		if err = f.spill(); err != nil {
			return 0, err
		}
	}
	return f.File.Write(p)
}
func (f *File) WriteAt(p []byte, offset int64) (int, error) {
	if f.closed {
		return 0, os.ErrClosed
	}
	if offset < 0 || offset+int64(len(p)) < offset {
		return 0, errors.New("invalid write offset")
	}
	if err := f.grow(offset + int64(len(p))); err != nil {
		return 0, err
	}
	if f.File == nil {
		if offset+int64(len(p)) <= int64(cap(f.buffer)) {
			end := int(offset) + len(p)
			if end > len(f.buffer) {
				f.buffer = f.buffer[:end]
			}
			return copy(f.buffer[offset:], p), nil
		}
		if err := f.spill(); err != nil {
			return 0, err
		}
	}
	return f.File.WriteAt(p, offset)
}
func (f *File) Close() error {
	f.once.Do(func() {
		f.closed = true
		if f.File != nil {
			f.err = errors.Join(f.File.Close(), os.Remove(f.Name()))
		}
		f.buffer = nil
		if f.release != nil {
			f.release()
			f.release = nil
		}
		f.disk.mu.Lock()
		f.disk.used -= f.size
		f.disk.mu.Unlock()
	})
	return f.err
}
