package server

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SSEClient connects to the broker's SSE stream and delivers events via callback.
type SSEClient struct {
	brokerURL string
	peerID    string
	onEvent   func(event, data string)
	done      chan struct{}
	once      sync.Once
}

// NewSSEClient creates an SSE client that connects to the broker stream.
func NewSSEClient(brokerURL, peerID string, onEvent func(event, data string)) *SSEClient {
	return &SSEClient{
		brokerURL: brokerURL,
		peerID:    peerID,
		onEvent:   onEvent,
		done:      make(chan struct{}),
	}
}

// Start connects to the SSE stream. Reconnects on failure.
func (c *SSEClient) Start() {
	go c.run()
}

// Stop closes the SSE connection.
func (c *SSEClient) Stop() {
	c.once.Do(func() { close(c.done) })
}

func (c *SSEClient) run() {
	for {
		select {
		case <-c.done:
			return
		default:
		}

		err := c.connect()
		if err != nil {
			log.Printf("SSE connection error: %v, reconnecting in 2s...", err)
		}

		select {
		case <-c.done:
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *SSEClient) connect() error {
	url := fmt.Sprintf("%s/stream/%s", c.brokerURL, c.peerID)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SSE stream returned %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	var eventType string

	for scanner.Scan() {
		select {
		case <-c.done:
			return nil
		default:
		}

		line := scanner.Text()

		if line == "" {
			eventType = ""
			continue
		}

		if strings.HasPrefix(line, ":") {
			// Comment (keepalive)
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if eventType != "" && c.onEvent != nil {
				c.onEvent(eventType, data)
			}
			continue
		}
	}

	return scanner.Err()
}
