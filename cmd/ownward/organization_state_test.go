package main

import (
	"context"
	"testing"

	"github.com/HJSunDev/ownward/internal/codexplugin"
)

func TestOrganizationJournalRecoversAndSharesLimits(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	a, e := openOrganizationJournal(root)
	if e != nil {
		t.Fatal(e)
	}
	b, e := openOrganizationJournal(root)
	if e != nil {
		t.Fatal(e)
	}
	defer b.close()
	if e = a.heartbeat(ctx, true); e != nil {
		t.Fatal(e)
	}
	if active, e := b.foreground(ctx); e != nil || !active {
		t.Fatal("foreground not shared", e)
	}
	p := codexplugin.OrganizationProfile{MaxAttempts: 2, MaxTokens: 100}
	if ok, _, e := a.begin(ctx, "system:principal", "work", p); e != nil || !ok {
		t.Fatal(e)
	}
	if e = a.save(ctx, "system:principal", "work", "running", 0, codexplugin.OrganizationUsage{TotalTokens: 20}); e != nil {
		t.Fatal(e)
	}
	a.close()
	if active, e := b.foreground(ctx); e != nil || active {
		t.Fatal("stopped connector retains foreground", e)
	}
	ok, prior, e := b.begin(ctx, "system:principal", "work", p)
	if e != nil || !ok || prior != 20 {
		t.Fatal("interrupted work not recoverable", ok, prior, e)
	}
	if e = b.save(ctx, "system:principal", "work", "accepted", prior, codexplugin.OrganizationUsage{TotalTokens: 15}); e != nil {
		t.Fatal(e)
	}
	var unknown, tokens int
	if e = b.db.QueryRow("SELECT unknown,tokens FROM executions").Scan(&unknown, &tokens); e != nil || unknown != 1 || tokens != 35 {
		t.Fatal("lost incomplete-call warning or usage", unknown, tokens, e)
	}
	if ok, _, e = b.begin(ctx, "system:principal", "work", p); e != nil || ok {
		t.Fatal("restart resets attempts", e)
	}
	if ok, _, e = b.begin(ctx, "system:principal", "other", p); e != nil || !ok {
		t.Fatal("failed work blocks other work", e)
	}
}
