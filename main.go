package main

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/widget"
)

var (
	srcBGPat = regexp.MustCompile(`<image[^>]*id="src-bg"[^>]*>`)
	annotTpl = template.Must(template.New("").Funcs(map[string]any{
		"stripunit": func(v string) string {
			return strings.TrimRight(v, "x%npiemtc")
		},
	}).Parse(`<?xml version="1.0" encoding="UTF-8" standalone="no"?>
<svg
   width="{{.Width}}"
   height="{{.Height}}"
   viewBox="{{.ViewBox}}"
   version="1.1"
   xmlns:xlink="http://www.w3.org/1999/xlink"
   xmlns="http://www.w3.org/2000/svg"
   xmlns:sodipodi="http://sodipodi.sourceforge.net/DTD/sodipodi-0.dtd"
   xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape"
   xmlns:svg="http://www.w3.org/2000/svg">
  <g
     inkscape:label="Layer 1"
     inkscape:groupmode="layer"
     id="layer1">
    <image
       id="src-bg"
       preserveAspectRatio="none"
       width="{{.Width | stripunit}}"
       height="{{.Height | stripunit}}"
       style="image-rendering:optimizeQuality"
       xlink:href="{{.Href}}"
       sodipodi:insensitive="true"
       inkscape:svg-dpi="300"
       x="0"
       y="0" />
  </g>
</svg>
`))

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
	sign := sha256.Sum256([]byte(fmt.Sprintf("%s,%d,%d,%s", stat.Size(), stat.ModTime(), tail)))
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

	if b, err := os.ReadFile(thumbPath); err == nil {
		w.Write(b)
		return
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

	// Serve the image

	b, err := os.ReadFile(thumbPath)
	if err != nil {
		log.Printf("failed to serve thumb '%s': %s", thumbPath, err)
		w.WriteHeader(http.StatusInternalServerError)
	}
	w.Write(b)
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
	//    b. Export the (a) to a PDF in cache.
	//    c. Return response to frontend.

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Add("Content-Type", "application/json")

	annotId := r.URL.Query().Get("id")
	path := r.URL.Query().Get("path")
	page := r.URL.Query().Get("page")
	if _, err := strconv.Atoi(page); err != nil || path == "" || annotId == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	_ = os.MkdirAll(cacheRoot, 0750)

	err := func() error {

		// Export PDF page to SVG

		srcPagePath := filepath.Join(cacheRoot, "annot-src-page-"+annotId+".svg")
		cmd := exec.Command("inkscape", "--pdf-page="+page, "--export-type=svg",
			"--pdf-poppler", "--export-filename="+srcPagePath, path)
		if _, err := cmd.Output(); err != nil {
			return fmt.Errorf("failed to convert page %s of '%s' to svg at '%s': %s", page, path, srcPagePath, err)
		}

		// Create a new SVG with above as background

		svgPageSpecs := struct {
			Width   string `xml:"width,attr"`
			Height  string `xml:"height,attr"`
			ViewBox string `xml:"viewBox,attr"`
			Href    string `xml:"-"`
		}{}
		f, err := os.Open(srcPagePath)
		if err != nil {
			return fmt.Errorf("failed to open '%s': %s", srcPagePath, err)
		}
		if err := xml.NewDecoder(f).Decode(&svgPageSpecs); err != nil {
			f.Close()
			return fmt.Errorf("failed to convert page %s of '%s' to svg at '%s': %s", page, path, srcPagePath, err)
		}
		f.Close()

		annotPath := filepath.Join(cacheRoot, "annot-"+annotId+".svg")
		f, err = os.Create(annotPath)
		if err != nil {
			return fmt.Errorf("failed to create '%s': %s", annotPath, err)
		}

		svgPageSpecs.Href = srcPagePath
		if err := annotTpl.Execute(f, svgPageSpecs); err != nil {
			f.Close()
			return fmt.Errorf("failed to write to '%s': %s", annotPath, err)
		}
		f.Close()

		// Run Inkscape in GUI mode to edit the annotation file

		beforeEditStat, err := os.Stat(annotPath)
		if err != nil {
			return fmt.Errorf("failed to stat '%s': %s", annotPath, err)
		}

		if _, err := exec.Command("inkscape", annotPath).Output(); err != nil {
			return fmt.Errorf("inkscape exited with error while editing '%s': %s", annotPath, err)
		}

		afterEditStat, err := os.Stat(annotPath)
		if err != nil {
			return fmt.Errorf("failed to stat '%s': %s", annotPath, err)
		}

		// Unmodified annotation file results in immediate response back to client

		if afterEditStat.ModTime() == beforeEditStat.ModTime() {
			json.NewEncoder(w).Encode(struct {
				Annotated bool `json:"annotated"`
			}{})
			return nil
		}

		// Otherwise, remove the background from the annotated file

		b, err := ioutil.ReadFile(annotPath)
		if err != nil {
			return fmt.Errorf("failed to read back '%s': %s", annotPath, err)
		}
		b = srcBGPat.ReplaceAll(b, nil)
		if err := ioutil.WriteFile(annotPath, b, 0644); err != nil {
			return fmt.Errorf("failed to write back '%s': %s", annotPath, err)
		}

		// Convert the SVG annotation to PDF

		annotPDFPath := filepath.Join(cacheRoot, "annot-"+annotId+".pdf")
		cmd = exec.Command("inkscape", "--export-type=pdf", "--export-filename="+annotPDFPath, annotPath)
		if _, err := cmd.Output(); err != nil {
			return fmt.Errorf("failed to convert annotation SVG to PDF at '%s': %s", annotPDFPath, cmdErr(err))
		}

		json.NewEncoder(w).Encode(struct {
			Annotated bool   `json:"annotated"`
			Path      string `json:"path"`
		}{true, annotPDFPath})

		return nil
	}()

	if err != nil {
		json.NewEncoder(w).Encode(struct {
			Error string `json:"error"`
		}{err.Error()})
	}
}

func handleSave(w http.ResponseWriter, r *http.Request) {
}

func run() error {

	// Init cache

	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("cannot get user cache dir: %s", err)
	}
	cacheRoot = filepath.Join(userCacheDir, "pdfrankestein")
	files, _ := ioutil.ReadDir(cacheRoot)
	for _, f := range files {
		_ = os.Remove(filepath.Join(cacheRoot, f.Name()))
	}

	a := app.New()
	w := a.NewWindow("Hello World")

	w.SetContent(widget.NewLabel("Hello World!"))
	w.ShowAndRun()

	return nil
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("pdfrankestein: ")
	flag.Parse()
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
