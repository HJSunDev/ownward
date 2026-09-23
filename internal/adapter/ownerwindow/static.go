package ownerwindow

import "embed"

// The owner window has no external asset or frontend build dependencies.
//
//go:embed static/index.html
var landing []byte

//go:embed static/*
var assets embed.FS

func staticAsset(path string) ([]byte, string, bool) {
	if path == "" {
		return landing, "text/html; charset=utf-8", true
	}
	allowed := map[string]string{"bootstrap.js": "text/javascript", "api.js": "text/javascript", "editor.js": "text/javascript", "app.js": "text/javascript", "ui.js": "text/javascript", "window.css": "text/css"}
	t, ok := allowed[path]
	if !ok {
		return nil, "", false
	}
	b, e := assets.ReadFile("static/" + path)
	return b, t + "; charset=utf-8", e == nil
}
