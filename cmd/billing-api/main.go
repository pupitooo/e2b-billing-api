package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"e2b/billing-api/internal/httpapi"
	"e2b/billing-api/internal/inbox"
)

func main() {
	startup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := inbox.OpenPool(startup, os.Getenv("DATABASE_URL"))
	cancel()
	if err != nil {
		log.Fatalf("Initialize inbox: %v", err)
	}
	defer pool.Close()

	server := &http.Server{
		Addr:              ":8080",
		Handler:           httpapi.NewHandler(inbox.NewPostgres(pool)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf("Billing API listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
