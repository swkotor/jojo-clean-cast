package events

import (
	"fmt"
	"sync"
	"time"
)

// Event is a single log entry shown in the dashboard activity panel
type Event struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"` // "info" or "error"
	Message string    `json:"message"`
}

const maxEvents = 250

var (
	mu     sync.Mutex
	buffer []Event
)

func add(level, format string, args ...interface{}) {
	mu.Lock()
	defer mu.Unlock()
	buffer = append(buffer, Event{
		Time:    time.Now(),
		Level:   level,
		Message: fmt.Sprintf(format, args...),
	})
	if len(buffer) > maxEvents {
		buffer = buffer[len(buffer)-maxEvents:]
	}
}

// Info records an informational event
func Info(format string, args ...interface{}) {
	add("info", format, args...)
}

// Error records an error event
func Error(format string, args ...interface{}) {
	add("error", format, args...)
}

// List returns events newest-first
func List() []Event {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Event, len(buffer))
	for i, e := range buffer {
		out[len(buffer)-1-i] = e
	}
	return out
}
