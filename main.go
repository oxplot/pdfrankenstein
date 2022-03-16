package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"net/http"

	"github.com/gorilla/csrf"
	"github.com/pkg/browser"
)

func handleHome(w http.ResponseWriter, r *http.Request) {
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
}

func handleThumb(w http.ResponseWriter, r *http.Request) {
}

func handleAnnotate(w http.ResponseWriter, r *http.Request) {
}

func handleSave(w http.ResponseWriter, r *http.Request) {
}

func run() error {
	csrfSecret := make([]byte, 32)
	if _, err := rand.Read(csrfSecret); err != nil {
		return fmt.Errorf("failed to generate CSRF secret: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleHome)
	mux.HandleFunc("/info", handleInfo)
	mux.HandleFunc("/thumb", handleThumb)
	mux.HandleFunc("/annotate", handleAnnotate)
	mux.HandleFunc("/save", handleSave)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to listen on localhost port: %w", err)
	}
	defer l.Close()
	browser.OpenURL("http://" + l.Addr().String())
	log.Printf("Listening on http://%s/", l.Addr())

	CSRF := csrf.Protect(csrfSecret, csrf.FieldName("csrf"), csrf.CookieName("csrf"))
	return http.Serve(l, CSRF(mux))
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("pdfrankestein: ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
