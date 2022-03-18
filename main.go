package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"os/signal"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/oxplot/pdfrankestein/session"
)

const (
	progName = "PDFrankestein"
)

var (
	emptyImg = image.NewRGBA(image.Rect(0, 0, 1, 1))
)

type PageGrid struct {
	labels []*widget.Label
	thumbs []*canvas.Image
	root   *fyne.Container
}

func NewPageGrid(pageCount int, tapHandler func(page int)) *PageGrid {
	labels := make([]*widget.Label, pageCount)
	thumbs := make([]*canvas.Image, pageCount)
	root := container.NewGridWrap(fyne.NewSize(150, 150))
	for i := range labels {
		labels[i] = widget.NewLabel(gridPageLabel(i, ""))
		labels[i].Alignment = fyne.TextAlignCenter
		thumbs[i] = canvas.NewImageFromImage(emptyImg)
		thumbs[i].FillMode = canvas.ImageFillContain
		var button *widget.Button
		func(i int) { button = widget.NewButton("", func() { tapHandler(i) }) }(i)
		root.Add(container.NewBorder(nil, labels[i], nil, nil, container.NewMax(thumbs[i], button)))
	}
	return &PageGrid{labels, thumbs, root}
}

func (g *PageGrid) Root() *fyne.Container {
	return g.root
}

func (g *PageGrid) SetThumbnail(page int, img image.Image) {
	g.thumbs[page].Image = img
}

func gridPageLabel(page int, note string) string {
	return fmt.Sprintf("%d %s", page+1, note)
}

func (g *PageGrid) SetNote(page int, note string) {
	g.labels[page].SetText(gridPageLabel(page, note))
}

func run() error {

	ap := app.New()
	win := ap.NewWindow(progName)

	var sess *session.Session
	sig := make(chan os.Signal, 1)
	done := make(chan struct{}, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		select {
		case <-sig:
			break
		case <-done:
			break
		}
		if sess != nil {
			sess.Close()
		}
		win.Close()
	}()

	fileNameLabel := widget.NewLabel("abc.pdf")
	fileNameLabel.TextStyle.Bold = true
	filePathLabel := widget.NewLabel("/home/...")
	filePathLabel.Wrapping = fyne.TextWrapBreak

	var openedContent *fyne.Container

	editingMsg := container.NewCenter(widget.NewLabel("Annotate in Inkscape.\nOnce done, save and close Inkscape and continue here."))
	savingMsg := container.NewCenter(widget.NewLabel("Saving ..."))
	gridScroll := container.NewVScroll(widget.NewLabel(""))

	startContent := container.NewCenter(widget.NewButton("Open PDF File", func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, err error) {

			// Get the file path

			if err != nil || r == nil {
				dialog.ShowError(err, win)
				return
			}
			r.Close()
			path := r.URI().String()
			if !strings.HasPrefix(path, "file://") {
				dialog.ShowError(fmt.Errorf("invalid file selected"), win)
				return
			}
			path = strings.TrimPrefix(path, "file://")

			// Create a new annotation session and page grid

			sess, err = session.New(path)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			var grid *PageGrid
			grid = NewPageGrid(sess.PageCount(), func(page int) {
				win.SetContent(editingMsg)
				defer win.SetContent(openedContent)

				modified, err := sess.Annotate(page)
				if err != nil {
					dialog.ShowError(err, win)
					return
				}
				if modified {
					grid.SetThumbnail(page, nil)
				}
				if sess.IsAnnotated(page) {
					grid.SetNote(page, "(annotated)")
				} else {
					grid.SetNote(page, "")
				}
			})

			gridScroll.Content = grid.Root()

			fileNameLabel.SetText(r.URI().Name())
			filePathLabel.SetText(path)
			win.SetContent(openedContent)

		}, win)
	}))

	openedContent = container.NewBorder(
		container.NewBorder(
			nil, nil, nil,
			container.NewHBox(
				widget.NewButton("Save", func() {
					dialog.ShowFileSave(func(w fyne.URIWriteCloser, err error) {
						if err != nil {
							dialog.ShowError(err, win)
							return
						}
						w.Close()
						path := w.URI().String()
						if !strings.HasPrefix(path, "file://") {
							dialog.ShowError(fmt.Errorf("invalid file selected"), win)
							return
						}
						path = strings.TrimPrefix(path, "file://")
						win.SetContent(savingMsg)
						if err := sess.Save(path); err != nil {
							dialog.ShowError(err, win)
						}
						win.SetContent(openedContent)
					}, win)
				}),
				widget.NewButton("Close", func() {
					gridScroll.Content = widget.NewLabel("")
					sess.Close()
					sess = nil
					win.SetContent(startContent)
				}),
			),
			container.NewVBox(
				fileNameLabel,
				filePathLabel,
			),
		),
		nil, nil, nil,
		gridScroll,
	)

	win.Resize(fyne.NewSize(600, 500))
	win.SetContent(startContent)
	win.ShowAndRun()

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
