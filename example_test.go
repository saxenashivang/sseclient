package sseclient

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"
)

type ChatResponse struct {
	Text     string `json:"text"`
	ThreadID string `json:"threadId"`
	Sources  []struct {
		FileID       string `json:"uuid"`
		FileName     string `json:"originalName"`
		FileUrl      string `json:"file"`
		LocLinesFrom int    `json:"loc.lines.from"`
		LocLinesTo   int    `json:"loc.lines.to"`
		Namespace    string `json:"namespace"`
		Source       string `json:"source"`
	} `json:"sources"`
	SQLQuery string `json:"sqlQuery"`
}

func Example() {
	req, _ := http.NewRequest("GET", "http://localhost:3000", nil)
	c := New(req, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// Use the function signature expected by c.Start
	eventHandler := func(event *Event) error {
		log.Printf("eventHandler: %v", event)
		// Log the raw event data for debugging
		log.Printf("Raw Event: %s : %s : %d bytes", event.ID, event.Event, len(event.Data))

		// Unmarshal the JSON within the 'data' field into ChatResponse
		var chatResponse ChatResponse
		if err := json.Unmarshal(event.Data, &chatResponse); err != nil {
			log.Printf("Error unmarshaling JSON: %s", err)
			return err
		}
		log.Println(chatResponse.Text)
		return nil
	}

	errorHandler := func(err error) error {
		log.Println("errorHandler", err)
		return err
	}

	log.Printf("Starting SSE client: HttpRequest: %v, Headers: %v, Retry: %v, VerboseStatusCodes: %v",
		c.HttpRequest, c.Headers, c.Retry, c.VerboseStatusCodes)
	c.Start(ctx, eventHandler, errorHandler)
}
