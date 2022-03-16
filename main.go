package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/gorilla/csrf"
	"github.com/pkg/browser"
)

var (
	autoOpenBrowser = flag.Bool("auto-open-browser", true, "auto open browser")
	enableCSRF      = flag.Bool("csrf", true, "enable CSRF protection")
	listen          = flag.String("listen", "127.0.0.1:0", "host:port to listen on")
)

type PDFInfo struct {
	PageCount int `json:"pageCount"`
}

func cmdErr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return errors.New(string(exitErr.Stderr))
	}
	return err
}

func handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	fmt.Fprintf(w, "CSRF Token: %s", csrf.Token(r))
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	path := r.PostForm.Get("path")
	if path == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	out, err := exec.Command("qpdf", "--show-npages", path).Output()
	if err != nil {
		json.NewEncoder(w).Encode(struct {
			Error string `json:"error"`
		}{cmdErr(err).Error()})
		return
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		log.Printf("cannot convert page count: %w", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(PDFInfo{PageCount: p})
}

func handleThumb(w http.ResponseWriter, r *http.Request) {
}

func handleAnnotate(w http.ResponseWriter, r *http.Request) {
}

func handleSave(w http.ResponseWriter, r *http.Request) {
}

func onlyPost(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

func run() error {
	csrfSecret := make([]byte, 32)
	if _, err := rand.Read(csrfSecret); err != nil {
		return fmt.Errorf("failed to generate CSRF secret: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleHome)
	mux.HandleFunc("/info", onlyPost(handleInfo))
	mux.HandleFunc("/thumb", onlyPost(handleThumb))
	mux.HandleFunc("/annotate", onlyPost(handleAnnotate))
	mux.HandleFunc("/save", onlyPost(handleSave))

	l, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("failed to listen on localhost port: %w", err)
	}
	defer l.Close()

	log.Printf("Listening on http://%s/", l.Addr())
	if *autoOpenBrowser {
		go browser.OpenURL("http://" + l.Addr().String())
	}

	h := http.Handler(mux)
	if *enableCSRF {
		h = csrf.Protect(csrfSecret, csrf.FieldName("csrf"), csrf.CookieName("csrf"))(h)
	}
	return http.Serve(l, h)
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("pdfrankestein: ")
	flag.Parse()
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
