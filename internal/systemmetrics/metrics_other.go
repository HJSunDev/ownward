//go:build !windows

package systemmetrics

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
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

func SampleProcess(processID int) (uint64, time.Duration, error) {
	output, err := exec.Command("ps", "-o", "rss=", "-o", "time=", "-p", strconv.Itoa(processID)).Output()
	if err != nil {
		return 0, 0, fmt.Errorf("读取进程 %d 指标: %w", processID, err)
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("进程 %d 指标格式无效", processID)
	}
	rssKiB, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("解析进程 %d 常驻内存: %w", processID, err)
	}
	cpu, err := parsePSCPUTime(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("解析进程 %d CPU 时间: %w", processID, err)
	}
	return rssKiB * 1024, cpu, nil
}

func parsePSCPUTime(value string) (time.Duration, error) {
	days := int64(0)
	if dayPart, remainder, found := strings.Cut(value, "-"); found {
		parsed, err := strconv.ParseInt(dayPart, 10, 64)
		if err != nil {
			return 0, err
		}
		days = parsed
		value = remainder
	}
	parts := strings.Split(value, ":")
	if len(parts) != 2 && len(parts) != 3 {
		return 0, fmt.Errorf("不支持的时间 %q", value)
	}
	seconds, err := strconv.ParseFloat(parts[len(parts)-1], 64)
	if err != nil {
		return 0, err
	}
	minutes, err := strconv.ParseInt(parts[len(parts)-2], 10, 64)
	if err != nil {
		return 0, err
	}
	hours := int64(0)
	if len(parts) == 3 {
		hours, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return 0, err
		}
	}
	return time.Duration(float64((days*24+hours)*3600+minutes*60)*float64(time.Second) + seconds*float64(time.Second)), nil
}
