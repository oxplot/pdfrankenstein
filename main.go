package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pkg/browser"
)

var (
	autoOpenBrowser = flag.Bool("auto-open-browser", true, "auto open browser")
	requireAuth     = flag.Bool("auth", true, "require authentication")
	listen          = flag.String("listen", "127.0.0.1:0", "host:port to listen on")
	authToken       string

	cacheRoot string
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
	fmt.Fprintf(w, "Auth Token: %s", authToken)
}

func handleInfo(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
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
	w.Header().Add("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PDFInfo{PageCount: p})
}

func handleThumb(w http.ResponseWriter, r *http.Request) {
}

func handleAnnotate(w http.ResponseWriter, r *http.Request) {
}

func handleSave(w http.ResponseWriter, r *http.Request) {
}

func authMiddleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Query().Get("auth") == authToken {
			h.ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
}

func run() error {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("cannot get user cache dir: %w", err)
	}
	cacheRoot = filepath.Join(userCacheDir, "pdfrankestein")

	authBytes := make([]byte, 32)
	if _, err := rand.Read(authBytes); err != nil {
		return fmt.Errorf("failed to generate auth token: %w", err)
	}
	authToken = hex.EncodeToString(authBytes)

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleHome)
	mux.HandleFunc("/info", handleInfo)
	mux.HandleFunc("/thumb", handleThumb)
	mux.HandleFunc("/annotate", handleAnnotate)
	mux.HandleFunc("/save", handleSave)

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
	if *requireAuth {
		h = authMiddleware(h)
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
