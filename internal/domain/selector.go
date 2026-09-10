package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// TextSelector 只定位原文；相邻文字用于消歧，不是另一份事实。
type TextSelector struct {
	Exact  string `json:"exact"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

func (s TextSelector) Resolve(content string) (int, int, error) {
	if strings.TrimSpace(s.Exact) == "" {
		return 0, 0, errors.New("说明定位的原文不能为空")
	}
	found := -1
	for offset := 0; offset <= len(content)-len(s.Exact); {
		n := strings.Index(content[offset:], s.Exact)
		if n < 0 {
			break
		}
		n += offset
		if strings.HasSuffix(content[:n], s.Prefix) && strings.HasPrefix(content[n+len(s.Exact):], s.Suffix) {
			if found >= 0 {
				return 0, 0, errors.New("说明原文不唯一，请提供相邻原文消歧")
			}
			found = n
		}
		offset = n + 1
	}
	if found < 0 {
		return 0, 0, errors.New("说明定位与当前正文失配，请同步修正定位")
	}
	start := utf8.RuneCountInString(content[:found])
	return start, start + utf8.RuneCountInString(s.Exact), nil
}
