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
	once          sync.Once
	err           error
}

// 覆盖 os.File 的快速复制接口，防止 io.Copy 绕过逐块额度检查。
func (f *File) ReadFrom(r io.Reader) (int64, error) {
	return io.CopyBuffer(struct{ io.Writer }{f}, r, make([]byte, 64*1024))
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
	if end <= f.size {
		return nil
	}
	n := end - f.size
	// 已保留额度还需对应磁盘余量；为当前写入之外保留8 MiB。
	if end > f.checked {
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
	offset, err := f.File.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if err = f.grow(offset + int64(len(p))); err != nil {
		return 0, err
	}
	return f.File.Write(p)
}
func (f *File) WriteAt(p []byte, offset int64) (int, error) {
	if err := f.grow(offset + int64(len(p))); err != nil {
		return 0, err
	}
	return f.File.WriteAt(p, offset)
}
func (f *File) Close() error {
	f.once.Do(func() {
		f.err = errors.Join(f.File.Close(), os.Remove(f.Name()))
		f.disk.mu.Lock()
		f.disk.used -= f.size
		f.disk.mu.Unlock()
	})
	return f.err
}
