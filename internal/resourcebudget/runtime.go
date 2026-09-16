package resourcebudget

import (
	"runtime/debug"
	"sync"
)

var runtimeLimitMu sync.Mutex

// LimitRuntime applies the dedicated Ownward process's GC working-set target.
// It supplements admission control; it is not an RSS limit or a model limit.
// A stricter operator-supplied GOMEMLIMIT remains in force.
func LimitRuntime(bytes int64) {
	runtimeLimitMu.Lock()
	defer runtimeLimitMu.Unlock()
	if bytes > 0 && debug.SetMemoryLimit(-1) > bytes {
		debug.SetMemoryLimit(bytes)
	}
}
