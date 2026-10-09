package main

import (
	"log"
	"net/http"
	"time"

	"e2b/billing-api/internal/httpapi"
)

func main() {
	server := &http.Server{
		Addr:              ":8080",
		Handler:           httpapi.NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("Billing API listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
