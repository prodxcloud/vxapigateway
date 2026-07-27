package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// LogLevel represents logging severity.
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l LogLevel) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "?"
	}
}

// Logger provides structured, level-aware logging with optional request ID and key-value pairs.
type Logger struct {
	mu        sync.Mutex
	out       io.Writer
	level     LogLevel
	useColor  bool
	component string
}

var (
	defaultLogger *Logger
	defaultLevel  = LevelInfo
)

func init() {
	defaultLogger = &Logger{
		out:      os.Stderr,
		level:    parseLogLevel(os.Getenv("LOG_LEVEL")),
		useColor: useColorEnv(),
	}
}

func parseLogLevel(s string) LogLevel {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "DEBUG":
		return LevelDebug
	case "INFO", "":
		return LevelInfo
	case "WARN", "WARNING":
		return LevelWarn
	case "ERROR":
		return LevelError
	default:
		return LevelInfo
	}
}

// ANSI color codes (only used when useColor is true)
const (
	ansiReset   = "\033[0m"
	ansiDim     = "\033[2m"
	ansiBold    = "\033[1m"
	ansiCyan    = "\033[36m"
	ansiGreen   = "\033[32m"
	ansiYellow  = "\033[33m"
	ansiRed     = "\033[31m"
	ansiMagenta = "\033[35m"
	ansiBlue    = "\033[34m"
	ansiGray    = "\033[90m"
)

func useColorEnv() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	if runtime.GOOS == "windows" {
		term := strings.ToLower(os.Getenv("TERM"))
		colorTerm := os.Getenv("COLORTERM")
		return strings.Contains(term, "color") || strings.Contains(term, "256") ||
			colorTerm == "truecolor" || colorTerm == "24bit" || colorTerm != ""
	}
	return true
}

// WithComponent returns a logger that prefixes all messages with the given component name.
func (l *Logger) WithComponent(component string) *Logger {
	return &Logger{
		out:       l.out,
		level:     l.level,
		useColor:  l.useColor,
		component: component,
	}
}

// Log writes a log line at the given level with optional requestID and key-value pairs.
func (l *Logger) Log(level LogLevel, requestID, msg string, kvs ...interface{}) {
	l.log(level, requestID, msg, kvs...)
}

func (l *Logger) log(level LogLevel, requestID, msg string, kvs ...interface{}) {
	if level < l.level {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().Format("2006-01-02 15:04:05.000")
	comp := l.component
	if comp == "" {
		comp = "gateway"
	}

	var b strings.Builder
	// [timestamp] LEVEL [component] [req=id] msg k1=v1 k2=v2
	if l.useColor {
		b.WriteString(ansiGray)
	}
	b.WriteString("[")
	b.WriteString(ts)
	b.WriteString("]")
	if l.useColor {
		b.WriteString(ansiReset)
		b.WriteString(" ")
		switch level {
		case LevelDebug:
			b.WriteString(ansiCyan)
		case LevelInfo:
			b.WriteString(ansiGreen)
		case LevelWarn:
			b.WriteString(ansiYellow)
		case LevelError:
			b.WriteString(ansiRed)
		}
	}
	b.WriteString(fmt.Sprintf("%-5s", level.String()))
	if l.useColor {
		b.WriteString(ansiReset)
		b.WriteString(" ")
		b.WriteString(ansiBlue)
	}
	b.WriteString("[")
	b.WriteString(comp)
	b.WriteString("]")
	if l.useColor {
		b.WriteString(ansiReset)
	}

	if requestID != "" {
		if l.useColor {
			b.WriteString(ansiDim)
		}
		b.WriteString(" req=")
		b.WriteString(requestID)
		if l.useColor {
			b.WriteString(ansiReset)
		}
	}
	b.WriteString(" ")
	b.WriteString(msg)

	for i := 0; i+1 < len(kvs); i += 2 {
		b.WriteString(" ")
		if l.useColor {
			b.WriteString(ansiDim)
		}
		b.WriteString(fmt.Sprint(kvs[i]))
		b.WriteString("=")
		if l.useColor {
			b.WriteString(ansiReset)
		}
		b.WriteString(fmt.Sprint(kvs[i+1]))
	}
	if len(kvs)%2 == 1 {
		b.WriteString(" ")
		b.WriteString(fmt.Sprint(kvs[len(kvs)-1]))
	}
	b.WriteString("\n")
	_, _ = l.out.Write([]byte(b.String()))
}

func (l *Logger) Debug(msg string, kvs ...interface{}) { l.log(LevelDebug, "", msg, kvs...) }
func (l *Logger) Info(msg string, kvs ...interface{})  { l.log(LevelInfo, "", msg, kvs...) }
func (l *Logger) Warn(msg string, kvs ...interface{})  { l.log(LevelWarn, "", msg, kvs...) }
func (l *Logger) Error(msg string, kvs ...interface{}) { l.log(LevelError, "", msg, kvs...) }

// WithRequestID logs with the given request ID (for request-scoped logs).
func (l *Logger) WithRequestID(requestID string) *requestLogger {
	return &requestLogger{Logger: l, requestID: requestID}
}

type requestLogger struct {
	*Logger
	requestID string
}

func (r *requestLogger) Debug(msg string, kvs ...interface{}) {
	r.Logger.Log(LevelDebug, r.requestID, msg, kvs...)
}
func (r *requestLogger) Info(msg string, kvs ...interface{}) {
	r.Logger.Log(LevelInfo, r.requestID, msg, kvs...)
}
func (r *requestLogger) Warn(msg string, kvs ...interface{}) {
	r.Logger.Log(LevelWarn, r.requestID, msg, kvs...)
}
func (r *requestLogger) Error(msg string, kvs ...interface{}) {
	r.Logger.Log(LevelError, r.requestID, msg, kvs...)
}

// Package-level helpers that use defaultLogger with optional component.
func logGateway() *Logger { return defaultLogger.WithComponent("gateway") }
func logCache() *Logger   { return defaultLogger.WithComponent("cache") }
func logRouter() *Logger  { return defaultLogger.WithComponent("router") }
func logAuth() *Logger    { return defaultLogger.WithComponent("auth") }
func logCircuit() *Logger { return defaultLogger.WithComponent("circuit") }
func logDDoS() *Logger    { return defaultLogger.WithComponent("ddos") }
func logConfig() *Logger  { return defaultLogger.WithComponent("config") }
func logAdmin() *Logger   { return defaultLogger.WithComponent("admin") }
func logHealth() *Logger  { return defaultLogger.WithComponent("health") }
func logTracer() *Logger  { return defaultLogger.WithComponent("tracer") }
func logProxy() *Logger   { return defaultLogger.WithComponent("proxy") }

// PrintBanner writes raw lines between separators with optional coloring.
func (l *Logger) PrintBanner(lines []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	sep := "------------------------------------------------------------"
	if l.useColor {
		_, _ = l.out.Write([]byte("\n" + ansiCyan + sep + ansiReset + "\n"))
		for i, line := range lines {
			if i == 0 {
				_, _ = l.out.Write([]byte("  " + ansiBold + line + ansiReset + "\n"))
			} else {
				_, _ = l.out.Write([]byte("  " + ansiDim + line + ansiReset + "\n"))
			}
		}
		_, _ = l.out.Write([]byte(ansiCyan + sep + ansiReset + "\n\n"))
	} else {
		_, _ = l.out.Write([]byte("\n" + sep + "\n"))
		for _, line := range lines {
			_, _ = l.out.Write([]byte("  " + line + "\n"))
		}
		_, _ = l.out.Write([]byte(sep + "\n\n"))
	}
}

// Banner prints a structured startup banner using the default logger.
func Banner(lines []string) {
	defaultLogger.PrintBanner(lines)
}
