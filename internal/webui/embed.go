// Package webui contains the dashboard assets embedded in the application.
package webui

import "embed"

// Files keeps the historic static/... filesystem paths used by HTTP handlers.
//
//go:embed static/index.html static/app.js static/styles.css static/model-picker.css static/project.css static/history-settings.css static/youtube.css
var Files embed.FS
