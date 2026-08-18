//go:build !windows

package systemmetrics

import (
	"runtime"
	"syscall"
	"time"
)

func RSSBytes() (uint64, error) {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return memory.Sys, nil
}

func CPUTime() (time.Duration, error) {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return 0, err
	}
	seconds := usage.Utime.Sec + usage.Stime.Sec
	microseconds := usage.Utime.Usec + usage.Stime.Usec
	return time.Duration(seconds)*time.Second + time.Duration(microseconds)*time.Microsecond, nil
}
