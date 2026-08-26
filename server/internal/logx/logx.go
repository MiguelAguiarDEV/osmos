package logx

import (
	"encoding/json"
	"os"
	"time"
)

// niveles: debug < info < error; off desactiva
var minLevel = 1 // 0=debug,1=info,2=error,3=off

func SetLevel(level string) {
	switch level {
	case "debug", "DEBUG":
		minLevel = 0
	case "info", "INFO", "":
		minLevel = 1
	case "error", "ERROR":
		minLevel = 2
	case "off", "OFF", "none", "NONE":
		minLevel = 3
	default:
		// desconocido → info
		minLevel = 1
	}
}

func Info(event string, fields map[string]any) {
	if minLevel <= 1 {
		logWith("info", event, fields)
	}
}
func Error(event string, fields map[string]any) {
	if minLevel <= 2 {
		logWith("error", event, fields)
	}
}

func logWith(level, event string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	fields["level"] = level
	fields["event"] = event
	_ = json.NewEncoder(os.Stdout).Encode(fields)
}
