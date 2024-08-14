package sseclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseEventRetry(t *testing.T) {
	r := bufio.NewReader(bytes.NewBufferString("retry: 100\n\n"))
	client := &Client{}

	_, err := client.parseEvent(r)
	assert.NoError(t, err)
	assert.Equal(t, 100*time.Millisecond, client.Retry)
}

func TestParseEventInvalidRetry(t *testing.T) {
	r := bufio.NewReader(bytes.NewBufferString("retry: ???\n\n"))
	client := &Client{}

	_, err := client.parseEvent(r)
	assert.NoError(t, err)
	assert.Equal(t, time.Duration(0), client.Retry)
}

func TestParseEvent(t *testing.T) {
	tests := []struct {
		data  string
		event *Event
		err   error
	}{
		{
			data: "\n\n",
			event: &Event{
				ID:    "",
				Event: "message",
				Data:  nil,
			},
			err: nil,
		},
		{
			data: "id: 123\n\n",
			event: &Event{
				ID:    "123",
				Event: "message",
				Data:  nil,
			},
			err: nil,
		},
		{
			data: "event: create\n\n",
			event: &Event{
				ID:    "",
				Event: "create",
				Data:  nil,
			},
			err: nil,
		},
		{
			data: "data: some data\n\n",
			event: &Event{
				ID:    "",
				Event: "message",
				Data:  []byte("some data"),
			},
			err: nil,
		},
		{
			data: "data: some data\ndata: multiline data\n\n",
			event: &Event{
				ID:    "",
				Event: "message",
				Data:  []byte("some data\nmultiline data"),
			},
			err: nil,
		},
		{
			data: "data: some data\r\ndata: multiline data\r\n\r\n",
			event: &Event{
				ID:    "",
				Event: "message",
				Data:  []byte("some data\nmultiline data"),
			},
			err: nil,
		},
		{
			data: ": some comment\n\n",
			event: &Event{
				ID:    "",
				Event: "message",
				Data:  nil,
			},
			err: nil,
		},
		{
			data: "unsupported field\n\n",
			event: &Event{
				ID:    "",
				Event: "message",
				Data:  nil,
			},
			err: nil,
		},
		{
			data: "id:123\nevent:create\ndata:this is some data\n\n",
			event: &Event{
				ID:    "123",
				Event: "create",
				Data:  []byte("this is some data"),
			},
			err: nil,
		},
		{
			data: "id: 123\nevent: create\ndata: this is some data\n\n",
			event: &Event{
				ID:    "123",
				Event: "create",
				Data:  []byte("this is some data"),
			},
			err: nil,
		},
		{
			data: `id: 123
event: create
data: this is some data
unsupported field
: some comment
data: multiline data

`,
			event: &Event{
				ID:    "123",
				Event: "create",
				Data:  []byte("this is some data\nmultiline data"),
			},
			err: nil,
		},
		{
			data:  "data: test", // missing \n to be complete event
			event: nil,
			err:   errMalformedEvent,
		},
		{
			data:  "",
			event: nil,
			err:   io.EOF,
		},
		{
			data:  "data: test\n",
			event: nil,
			err:   io.EOF,
		},
	}

	for _, test := range tests {
		r := bufio.NewReader(bytes.NewBufferString(test.data))
		client := &Client{}
		event, err := client.parseEvent(r)
		assert.Equal(t, test.event, event)
		assert.Equal(t, test.err, err)
	}
}

func sseHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/single-event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, "data: single event stream\n\n")
	})
	mux.HandleFunc("/500", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "oops 500", http.StatusInternalServerError)
	})
	mux.HandleFunc("/409", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "oops 409", http.StatusConflict)
	})

	return mux
}

func TestClientReconnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, "data: single event stream\n\n")
		w.(http.Flusher).Flush()
		// Close the connection after sending one event
		time.Sleep(time.Millisecond)
	}))
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL, nil)
	client := New(req, "")
	client.Retry = time.Millisecond // Set a short retry time for testing

	counter := 0
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	handler := func(e *Event) error {
		counter++
		if counter == 5 {
			cancel()
		}
		return nil
	}

	client.Start(ctx, handler, ReconnectOnError)

	if counter < 5 {
		t.Fatalf("expected to receive at least 5 events, received %d", counter)
	}
}

func TestClientError409(t *testing.T) {
	server := httptest.NewServer(sseHandler())
	defer server.Close()

	ok := false
	eventHandler := func(e *Event) error { return nil }
	errorHandler := func(err error) error {
		ok = true
		return errors.New("stop")
	}

	req, _ := http.NewRequest("GET", server.URL+"/409", nil)
	client := New(req, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	client.Start(ctx, eventHandler, errorHandler)

	if !ok {
		t.Fatalf("response code 409 should trigger a call to error handler")
	}
}

func TestClientEventHandlerErrorPropagation(t *testing.T) {
	server := httptest.NewServer(sseHandler())
	defer server.Close()

	parserErr := errors.New("fail always")
	streamErr := errors.New("stop the stream")

	var receivedByHandler error
	eventHandler := func(e *Event) error { return parserErr }
	errorHandler := func(err error) error {
		receivedByHandler = err
		return streamErr
	}

	req, _ := http.NewRequest("GET", server.URL+"/single-event", nil)
	client := New(req, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := client.Start(ctx, eventHandler, errorHandler)

	if err != streamErr {
		t.Fatalf("stream client dropped error handler error")
	}
	if receivedByHandler != parserErr {
		t.Fatalf("stream client did not pass parser error to error handler")
	}
}

func TestClientStream(t *testing.T) {
	server := httptest.NewServer(sseHandler())
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL+"/single-event", nil)
	client := New(req, "")
	client.Retry = 0

	ctx, stop := context.WithCancel(context.TODO())
	defer stop()
	var actual []StreamMessage
	for msg := range client.Stream(ctx, 0, "GET") {
		actual = append(actual, msg)
		stop()
	}

	expected := []StreamMessage{{
		Event: &Event{
			Event: "message",
			Data:  []byte("single event stream"),
		},
	}}
	assert.Equal(t, expected, actual)
}

func TestClientStreamError(t *testing.T) {
	server := httptest.NewServer(sseHandler())
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL+"/409", nil)
	client := New(req, "")
	client.Retry = 0

	ctx, stop := context.WithCancel(context.TODO())
	defer stop()
	var actual []StreamMessage
	for msg := range client.Stream(ctx, 0, "GET") {
		actual = append(actual, msg)
		stop()
	}

	expected := []StreamMessage{{
		Err: errors.New("bad response status code 409"),
	}}
	assert.Equal(t, expected, actual)
}

func TestReconnectAfterPartialEvent(t *testing.T) {
	ctx, stop := context.WithCancel(context.TODO())
	defer stop()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		var response string
		id := r.Header.Get("Last-Event-ID")
		switch id {
		case "": // first request
			response = "id: 1\ndata: message1\n\nid: 2\ndata: partial second message"
		case "1": // second request
			response = "id: 2\ndata: message2\n\n"
		case "2": // third request
			response = "id: 3\ndata: message3\n\n"
		default:
			stop()
		}
		fmt.Fprint(w, response)
	}))
	defer server.Close()

	req, _ := http.NewRequest("GET", server.URL, nil)
	client := New(req, "")
	client.Retry = time.Millisecond // Set a short retry time for testing

	var actual []StreamMessage
	for msg := range client.Stream(ctx, 0, "GET") {
		actual = append(actual, msg)
		if len(actual) == 4 {
			stop()
		}
	}

	expected := []StreamMessage{
		{
			Event: &Event{
				ID:    "1",
				Event: "message",
				Data:  []byte("message1"),
			},
		},
		{
			Err: errMalformedEvent,
		},
		{
			Event: &Event{
				ID:    "2",
				Event: "message",
				Data:  []byte("message2"),
			},
		},
		{
			Event: &Event{
				ID:    "3",
				Event: "message",
				Data:  []byte("message3"),
			},
		},
	}
	assert.Equal(t, expected, actual)
}
