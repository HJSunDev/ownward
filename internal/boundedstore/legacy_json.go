package boundedstore

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/resourcebudget"
	"github.com/HJSunDev/ownward/internal/streamjson"
)

// Reframe the original JSON log on disk; the common importer performs the
// same identity, dependency and vector checks as for the binary log.
func (s *Store) legacyJSONFile(ctx context.Context, source string) (*os.File, error) {
	input, e := os.Open(source)
	if e != nil {
		return nil, e
	}
	defer input.Close()
	f, e := os.CreateTemp(s.directory, ".legacy-json-*")
	if e != nil {
		return nil, e
	}
	defer os.Remove(f.Name())
	e = scanLegacyLines(ctx, input, 0, func(pos, length int64) error {
		d, e := streamjson.Parse(ctx, s.directory, io.NewSectionReader(input, pos, length), s.budget, 256*resourcebudget.MiB)
		if e != nil {
			return e
		}
		defer d.Close()
		schema, e := nodeString(d.Root(), "schema")
		if e != nil {
			return e
		}
		if schema != "ownward.derived/v2" {
			return errors.New("旧JSON派生格式无效")
		}
		var vector []byte
		if e = nodeDecode(d.Root(), "embedding_f32le", &vector); e != nil {
			return e
		}
		if len(vector) != 0 && len(vector) != 512*4 {
			return errors.New("旧JSON向量维度无效")
		}
		start, e := f.Seek(0, io.SeekCurrent)
		if e != nil {
			return e
		}
		var h [16]byte
		if _, e = f.Write(h[:]); e != nil {
			return e
		}
		crc := crc32.NewIEEE()
		size, e := copyContext(ctx, io.MultiWriter(f, crc), d.Root().Raw())
		if e != nil {
			return e
		}
		if _, e = io.MultiWriter(f, crc).Write(vector); e != nil {
			return e
		}
		if _, e = f.WriteString("DONE"); e != nil {
			return e
		}
		copy(h[:4], "OWD3")
		binary.LittleEndian.PutUint32(h[4:8], uint32(size))
		binary.LittleEndian.PutUint32(h[8:12], uint32(len(vector)))
		binary.LittleEndian.PutUint32(h[12:], crc.Sum32())
		_, e = f.WriteAt(h[:], start)
		return e
	})
	if e == nil {
		e = f.Sync()
	}
	e2 := f.Close()
	if e == nil {
		e = e2
	}
	if e != nil {
		return nil, e
	}
	path := filepath.Join(s.directory, "legacy-json.binlog")
	if e = replaceDurable(f.Name(), path); e != nil {
		return nil, e
	}
	return os.Open(path)
}
