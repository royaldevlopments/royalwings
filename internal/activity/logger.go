package activity

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/royaldevlopments/royalwings/internal/panel"
)

type Logger struct {
	mu       sync.Mutex
	logFile  *os.File
	encoder  *json.Encoder
	panel    *panel.Client
	buffer   []Event
	bufferMu sync.Mutex
	done     chan struct{}
}

type Event struct {
	Event      string                 `json:"event"`
	ServerUUID string                 `json:"server_uuid,omitempty"`
	UserUUID   string                 `json:"user_uuid,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
	Timestamp  string                 `json:"timestamp"`
	IP         string                 `json:"ip,omitempty"`
	UserAgent  string                 `json:"user_agent,omitempty"`
}

func NewLogger(logPath string, panel *panel.Client) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	l := &Logger{
		logFile: f,
		encoder: json.NewEncoder(f),
		panel:   panel,
		buffer:  make([]Event, 0, 100),
		done:    make(chan struct{}),
	}

	go l.flushLoop()

	return l, nil
}

func (l *Logger) Close() error {
	l.flushBuffer()
	close(l.done)
	return l.logFile.Close()
}

func (l *Logger) Write(event Event) {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.encoder.Encode(event); err != nil {
		log.Printf("Failed to write activity event: %v", err)
	}

	l.bufferMu.Lock()
	l.buffer = append(l.buffer, event)
	if len(l.buffer) >= 50 {
		l.bufferMu.Unlock()
		l.flushBuffer()
	} else {
		l.bufferMu.Unlock()
	}
}

func (l *Logger) WriteServerEvent(serverUUID, eventName string, metadata map[string]interface{}) {
	l.Write(Event{
		Event:      eventName,
		ServerUUID: serverUUID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Metadata:   metadata,
	})
}

func (l *Logger) WriteUserEvent(userUUID, serverUUID, eventName string, metadata map[string]interface{}) {
	l.Write(Event{
		Event:      eventName,
		ServerUUID: serverUUID,
		UserUUID:   userUUID,
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Metadata:   metadata,
	})
}

func (l *Logger) flushBuffer() {
	l.bufferMu.Lock()
	events := l.buffer
	l.buffer = make([]Event, 0, 100)
	l.bufferMu.Unlock()

	if len(events) == 0 {
		return
	}

	panelEvents := make([]panel.ActivityEvent, len(events))
	for i, e := range events {
		panelEvents[i] = panel.ActivityEvent{
			Event:      e.Event,
			ServerUUID: e.ServerUUID,
			UserUUID:   e.UserUUID,
			Metadata:   e.Metadata,
			Timestamp:  e.Timestamp,
		}
	}

	if err := l.panel.SubmitActivity(panelEvents); err != nil {
		log.Printf("Failed to submit activity to panel: %v", err)
	}
}

func (l *Logger) flushLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-l.done:
			return
		case <-ticker.C:
			l.flushBuffer()
		}
	}
}
