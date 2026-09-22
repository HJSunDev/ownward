package main

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The host form is just another presentation of the same authority. While it
// is open, a window decision cancels it and resumes the original tool call.
// The authoritative decide operation arbitrates late/concurrent submissions.
func awaitOwnerDecision(ctx context.Context, session *mcp.ServerSession, message string, read func(context.Context) (string, error), decide func(context.Context, bool) (string, error)) (string, error) {
	finished := func(state string) bool { return state != "" && state != "awaiting_approval" && state != "pending" }
	state, e := read(ctx)
	if e != nil || finished(state) {
		return state, e
	}
	confirmCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		accept bool
		err    error
	}
	answers := make(chan answer, 1)
	go func() { accept, e := hostConfirm(confirmCtx, session, message); answers <- answer{accept, e} }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case result := <-answers:
			if result.err != nil {
				// Check for the other presentation's decision before reporting an
				// unsupported/disconnected confirmation form.
				state, e = read(ctx)
				if e == nil && finished(state) {
					return state, nil
				}
				return "", result.err
			}
			return decide(ctx, result.accept)
		case <-ticker.C:
			state, e = read(ctx)
			if e != nil {
				return "", e
			}
			if finished(state) {
				return state, nil
			}
		}
	}
}

func acceptedDecision(state string) error {
	if state == "superseded" {
		return errors.New("操作目标已变化，本次操作未执行；请刷新后重新确认。")
	}
	if state == "declined" || state == "cancelled" {
		return errors.New("用户未批准该操作")
	}
	return nil
}
