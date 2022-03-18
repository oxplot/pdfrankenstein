package session

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
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
)

func cmdErr(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return errors.New(string(exitErr.Stderr))
	}
	return err
}

func fileCopy(src, dst string) error {
	fin, err := os.Open(src)
	if err != nil {
		return err
	}
	defer fin.Close()
	fout, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer fout.Close()
	_, err = io.Copy(fout, fin)
	return err
}

type Session struct {
	path      string
	pageCount int
	tmpDir    string
	annotated map[int]struct{}
}

func New(path string) (*Session, error) {

	// Get page count

	out, err := exec.Command("qpdf", "--show-npages", path).Output()
	if err != nil {
		return nil, cmdErr(err)
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, fmt.Errorf("cannot convert page count: %s", err)
	}

	// Create temp dir

	tmpDir, err := ioutil.TempDir("", "pdfrankestein-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %s", err)
	}

	// Make our own copy

	copyPath := filepath.Join(tmpDir, "src.pdf")
	if err := fileCopy(path, copyPath); err != nil {
		return nil, err
	}

	return &Session{copyPath, p, tmpDir, map[int]struct{}{}}, nil
}

func (s *Session) PageCount() int {
	return s.pageCount
}

func (s *Session) Thumbnail(page int) (string, error) {

	if page < 0 || page >= s.pageCount {
		panic("invalid page number")
	}

	thumbPath := s.thumbPath(page)

	// Serve from cache if available

	if _, err := os.Stat(thumbPath); err == nil {
		return thumbPath, nil
	}

	// Otherwise, run inkscape to generate image

	cmd := exec.Command("inkscape", "--pdf-page="+strconv.Itoa(page+1), "--export-type=png",
		"--export-area-page", "--export-dpi=20", "--pdf-poppler",
		"--export-background=white", "--export-filename="+thumbPath+".tmp", s.path)
	if _, err := cmd.Output(); err != nil {
		return "", fmt.Errorf("failed to generate thumb for page %s of '%s': %s", page, s.path, cmdErr(err))
	}
	_ = os.Rename(thumbPath+".tmp", thumbPath)

	return thumbPath, nil
}

func (s *Session) Annotate(page int) (bool, error) {

	if page < 0 || page >= s.pageCount {
		panic("invalid page number")
	}

	// Export PDF page to SVG (if needed)

	srcPath := s.srcPath(page)
	if _, err := os.Stat(srcPath); err != nil {
		cmd := exec.Command("inkscape", "--pdf-page="+strconv.Itoa(page+1), "--export-type=svg",
			"--pdf-poppler", "--export-filename="+srcPath+".svg", s.path)
		if _, err := cmd.Output(); err != nil {
			return false, fmt.Errorf("failed to convert page %s of '%s' to svg: %s", page, s.path, cmdErr(err))
		}
		_ = os.Rename(srcPath+".svg", srcPath)
	}

	// Create a new SVG with above as background (if needed)

	annotPath := s.annotPath(page)
	if _, err := os.Stat(annotPath); err != nil {
		pageSpecs := struct {
			Width   string `xml:"width,attr"`
			Height  string `xml:"height,attr"`
			ViewBox string `xml:"viewBox,attr"`
			Href    string `xml:"-"`
		}{}
		f, err := os.Open(srcPath)
		if err != nil {
			return false, fmt.Errorf("failed to open '%s': %s", srcPath, err)
		}
		if err := xml.NewDecoder(f).Decode(&pageSpecs); err != nil {
			f.Close()
			return false, fmt.Errorf("failed to parse svg at '%s': %s", srcPath, err)
		}
		f.Close()

		f, err = os.Create(annotPath + ".tmp")
		if err != nil {
			return false, fmt.Errorf("failed to create '%s': %s", annotPath, err)
		}

		pageSpecs.Href = srcPath
		if err := annotTpl.Execute(f, pageSpecs); err != nil {
			f.Close()
			return false, fmt.Errorf("failed to write to '%s': %s", annotPath, err)
		}
		f.Close()
		_ = os.Rename(annotPath+".tmp", annotPath)
	}

	// Run Inkscape in GUI mode to edit the annotation file

	beforeEditStat, err := os.Stat(annotPath)
	if err != nil {
		return false, fmt.Errorf("failed to stat '%s': %s", annotPath, err)
	}

	if _, err := exec.Command("inkscape", annotPath).Output(); err != nil {
		return false, fmt.Errorf("inkscape exited with error while editing '%s': %s", annotPath, err)
	}

	afterEditStat, err := os.Stat(annotPath)
	if err != nil {
		return false, fmt.Errorf("failed to stat '%s': %s", annotPath, err)
	}

	modified := afterEditStat.ModTime() != beforeEditStat.ModTime()
	if modified {
		_ = os.Remove(s.thumbPath(page))
		s.annotated[page] = struct{}{}
	}
	return modified, nil
}

func (s *Session) annotPath(page int) string {
	return filepath.Join(s.tmpDir, fmt.Sprintf("annot-%d.svg", page))
}

func (s *Session) srcPath(page int) string {
	return filepath.Join(s.tmpDir, fmt.Sprintf("src-%d.svg", page))
}

func (s *Session) thumbPath(page int) string {
	return filepath.Join(s.tmpDir, fmt.Sprintf("thumb-%d.png", page))
}

func (s *Session) IsAnnotated(page int) bool {
	if page < 0 || page >= s.pageCount {
		panic("invalid page number")
	}
	_, ok := s.annotated[page]
	return ok
}

func (s *Session) HasAnnotations() bool {
	return len(s.annotated) > 0
}

func (s *Session) Clear(page int) {
	if page < 0 || page >= s.pageCount {
		panic("invalid page number")
	}
	_ = os.Remove(s.annotPath(page))
	_ = os.Remove(s.thumbPath(page))
	delete(s.annotated, page)
}

func (s *Session) Save(path string) error {

	// Shortcut for when no page is annotated

	if len(s.annotated) == 0 {
		return fileCopy(s.path, path)
	}

	// Covert all annotated pages to PDF

	annotated := []int{}
	for i := 0; i < s.pageCount; i++ {
		if !s.IsAnnotated(i) {
			continue
		}
		annotated = append(annotated, i)

		annotPath := s.annotPath(i)

		// Remove the backgrounds

		b, err := ioutil.ReadFile(annotPath)
		if err != nil {
			return fmt.Errorf("failed to read back '%s': %s", annotPath, err)
		}
		b = srcBGPat.ReplaceAll(b, nil)
		if err := ioutil.WriteFile(annotPath+".cleaned.svg", b, 0644); err != nil {
			return fmt.Errorf("failed to write back '%s': %s", annotPath, err)
		}

		// Convert to PDF

		cmd := exec.Command("inkscape", "--export-type=pdf",
			"--export-filename="+annotPath+".pdf", annotPath+".cleaned.svg")
		if _, err := cmd.Output(); err != nil {
			return fmt.Errorf("failed to convert annotation SVG ('%s') to PDF: %s", annotPath, cmdErr(err))
		}
	}

	// Append all annotated PDFs into a single PDF

	overlayPath := filepath.Join(s.tmpDir, "overlay.pdf")

	args := []string{"--empty", "--pages"}
	for _, p := range annotated {
		args = append(args, s.annotPath(p)+".pdf")
	}
	args = append(args, "--", overlayPath)

	cmd := exec.Command("qpdf", args...)
	if _, err := cmd.Output(); err != nil {
		return fmt.Errorf("failed to merge annotated pages to '%s': %s", overlayPath, cmdErr(err))
	}

	// Overlay and create the final file

	finalPath := filepath.Join(s.tmpDir, "final.pdf")

	annotedStr := make([]string, len(annotated))
	for i, p := range annotated {
		annotedStr[i] = strconv.Itoa(p + 1)
	}
	pageRange := strings.Join(annotedStr, ",")

	cmd = exec.Command("qpdf", s.path, "--overlay", overlayPath, "--to="+pageRange, "--", finalPath)
	if _, err := cmd.Output(); err != nil {
		return fmt.Errorf("failed to overlay annotated pages to '%s': %s", finalPath, cmdErr(err))
	}

	return fileCopy(finalPath, path)
}

func (s *Session) Close() {
	files, _ := ioutil.ReadDir(s.tmpDir)
	for _, f := range files {
		_ = os.Remove(filepath.Join(s.tmpDir, f.Name()))
	}
	_ = os.Remove(s.tmpDir)
	s.annotated = nil
	s.tmpDir = ""
	s.pageCount = -1
	s.path = ""
}
