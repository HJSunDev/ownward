package core

import (
	"context"
	"errors"
	"sync"

	"github.com/HJSunDev/ownward/internal/contract"
)

// deliveryMaterials owns only undelivered copies, never durable source data.
type deliveryMaterials struct {
	mu      sync.Mutex
	check   func(context.Context) error
	copies  map[*materialCopy]struct{}
	invalid bool
}
type materialCopy struct {
	close func() error
	once  sync.Once
	err   error
}

func (m *materialCopy) release() error { m.once.Do(func() { m.err = m.close() }); return m.err }

func (s *StreamingAssets) trackResult(r *contract.StreamResult) {
	d := &deliveryMaterials{check: r.Check, copies: make(map[*materialCopy]struct{})}
	s.materialMu.Lock()
	if s.materials == nil {
		s.materials = make(map[*deliveryMaterials]struct{})
	}
	s.materials[d] = struct{}{}
	s.materialMu.Unlock()
	retain := func(close func() error) (func() error, error) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.invalid {
			return nil, errors.Join(errors.New("交付材料已失效"), close())
		}
		if e := d.check(context.Background()); e != nil {
			return nil, errors.Join(e, close())
		}
		copy := &materialCopy{close: close}
		d.copies[copy] = struct{}{}
		return func() error {
			d.mu.Lock()
			defer d.mu.Unlock()
			e := copy.release()
			delete(d.copies, copy)
			if len(d.copies) == 0 {
				d.invalid = true
				s.materialMu.Lock()
				delete(s.materials, d)
				s.materialMu.Unlock()
			}
			return e
		}, nil
	}
	// The result was built under the execution barrier. Check again at each
	// transport handoff, including one racing a completed forgetting operation.
	original := &materialCopy{close: r.Close}
	d.copies[original] = struct{}{}
	r.Close = func() error {
		d.mu.Lock()
		defer d.mu.Unlock()
		e := original.release()
		delete(d.copies, original)
		if len(d.copies) == 0 {
			d.invalid = true
			s.materialMu.Lock()
			delete(s.materials, d)
			s.materialMu.Unlock()
		}
		return e
	}
	r.Retain = retain
}

func (s *StreamingAssets) cleanDeliveryMaterials(all bool) error {
	// Executions finish their temporary parsing/building before cleanup can
	// complete. Network delivery is outside this barrier.
	s.deliveryMu.Lock()
	defer s.deliveryMu.Unlock()
	s.materialMu.Lock()
	list := make([]*deliveryMaterials, 0, len(s.materials))
	for d := range s.materials {
		list = append(list, d)
	}
	s.materialMu.Unlock()
	var result error
	for _, d := range list {
		d.mu.Lock()
		if all || d.check(context.Background()) != nil {
			d.invalid = true
			for c := range d.copies {
				result = errors.Join(result, c.release())
			}
			s.materialMu.Lock()
			delete(s.materials, d)
			s.materialMu.Unlock()
		}
		d.mu.Unlock()
	}
	return result
}
