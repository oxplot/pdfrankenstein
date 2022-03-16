package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
		log.Printf("cannot convert page count: %s", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	json.NewEncoder(w).Encode(PDFInfo{PageCount: p})
}

func calcPDFSignature(path string) (string, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	tail := make([]byte, 1024)
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := stat.Size() - int64(len(tail))
	if s < 0 {
		s = 0
	}
	if _, err := f.Seek(s, 0); err != nil {
		return "", err
	}
	if _, err := io.ReadFull(f, tail); err != nil {
		return "", err
	}
	sign := sha256.Sum256([]byte(fmt.Sprintf("%d,%d,%s", stat.Size(), stat.ModTime(), tail)))
	return fmt.Sprintf("%x", sign), nil
}

func handleThumb(w http.ResponseWriter, r *http.Request) {

	// Simple caching thumbnail generator:
	// 1. Make a unique signature from the given PDF file.
	// 2. Hash its last 1KB of data, its size and modified timestamp.
	// 3. Check cache if the hash+page is already available.
	// 3. If not, use inkscape to import the given PDF page and export it as a downsampled PNG.

	w.Header().Add("Content-Type", "image/png")

	path := r.URL.Query().Get("path")
	page := r.URL.Query().Get("page")
	hasBG := r.URL.Query().Get("bg") != ""
	if _, err := strconv.Atoi(page); path == "" || err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	sign, err := calcPDFSignature(path)
	if err != nil {
		log.Printf("cannot calc PDF signature for %s: %s", path, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	thumbPath := filepath.Join(cacheRoot, sign) + "-" + page
	if hasBG {
		thumbPath += "-bg"
	}
	thumbPath += ".png"

	// Serve from cache if available

	if f, err := os.Open(thumbPath); err == nil {
		if _, err := io.Copy(w, f); err == nil {
			f.Close()
			return
		}
		f.Close()
	}

	// Run inkscape to generate image

	exportOpacity := "0.0"
	if hasBG {
		exportOpacity = "1.0"
	}

	_ = os.MkdirAll(cacheRoot, 0750)
	cmd := exec.Command("inkscape", "--pdf-page="+page, "--export-type=png",
		"--export-area-page", "--export-dpi=20", "--pdf-poppler",
		"--export-background=white", "--export-background-opacity="+exportOpacity,
		"--export-filename="+thumbPath, path)
	if _, err := cmd.Output(); err != nil {
		log.Printf("failed to generate thumb for page %s of '%s' in '%s': %s", page, path, thumbPath, cmdErr(err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	f, err := os.Open(thumbPath)
	if err != nil {
		log.Printf("failed to open '%s': %s", thumbPath, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("failed to read '%s': %s", thumbPath, err)
	}
}

// handleAnnotate will open Inkscape with the given page of the given PDF file
// as a locked background over which the user can draw what they wish.
// After saving and closing Inkscape, the HTTP request completes and the frontend
// receives JSON {"annotated": bool, "path": string}. "annotated" is true if
// the file was saved (based on its modified timestamp). "path" is a path to the
// PDF of the annotated single page.
func handleAnnotate(w http.ResponseWriter, r *http.Request) {

	// 1. Inkscape export PDF page to SVG
	// 2. Create a new SVG with (1) and instructions linked as background images
	//    and locked.
	// 3. Run Inkscape in GUI mode to edit (2).
	// 4. Upon Inkscape exit, test if (2) was modified (based on filesystem timestamp).
	// 5. If modified:
	//    a. Update (2) removing the background images.
	//    b. Export the (a) to a temporary PDF.
	//    c. Use qpdf to overlay (b) on top of the original page
	// 6.

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
		return fmt.Errorf("cannot get user cache dir: %s", err)
	}
	cacheRoot = filepath.Join(userCacheDir, "pdfrankestein")

	authBytes := make([]byte, 32)
	if _, err := rand.Read(authBytes); err != nil {
		return fmt.Errorf("failed to generate auth token: %s", err)
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
		return fmt.Errorf("failed to listen on localhost port: %s", err)
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
