package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/oxplot/pdfrankenstein/session"
)

const (
	progName = "PDFrankenstein"
)

var (
	emptyImg = image.NewRGBA(image.Rect(0, 0, 1, 1))
)

type PageGrid struct {
	labels []*widget.Label
	thumbs []*canvas.Image
	clears []*widget.Button
	root   *fyne.Container
}

func NewPageGrid(pageCount int, tapHandler func(page int), clearHandler func(page int)) *PageGrid {
	labels := make([]*widget.Label, pageCount)
	thumbs := make([]*canvas.Image, pageCount)
	clears := make([]*widget.Button, pageCount)
	root := container.NewGridWrap(fyne.NewSize(150, 180))
	for i := range labels {
		labels[i] = widget.NewLabel(strconv.Itoa(i + 1))
		labels[i].Alignment = fyne.TextAlignCenter
		thumbs[i] = canvas.NewImageFromImage(emptyImg)
		thumbs[i].FillMode = canvas.ImageFillContain
		func(i int) { clears[i] = widget.NewButton("clear annots", func() { clearHandler(i) }) }(i)
		clears[i].Hide()
		var button *widget.Button
		func(i int) { button = widget.NewButton("", func() { tapHandler(i) }) }(i)
		root.Add(container.NewBorder(nil,
			container.NewBorder(nil, nil, nil, clears[i], labels[i]),
			nil, nil, container.NewMax(thumbs[i], button)))
	}
	return &PageGrid{labels, thumbs, clears, root}
}

func (g *PageGrid) Root() *fyne.Container {
	return g.root
}

func (g *PageGrid) SetThumbnail(page int, img image.Image) {
	g.thumbs[page].Image = img
}

func (g *PageGrid) SetAnnotated(page int, annotated bool) {
	if annotated {
		g.clears[page].Show()
	} else {
		g.clears[page].Hide()
	}
	g.root.Refresh()
}

func run() error {

	ap := app.New()
	win := ap.NewWindow(progName)

	var sess *session.Session
	sig := make(chan os.Signal, 1)
	runExit := make(chan struct{}, 1)
	done := make(chan struct{}, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		select {
		case <-sig:
			break
		case <-runExit:
			break
		}
		if sess != nil {
			sess.Close()
		}
		win.Close()
		close(done)
	}()
	defer func() {
		close(runExit)
		<-done
	}()

	fileNameLabel := widget.NewLabel("abc.pdf")
	fileNameLabel.TextStyle.Bold = true
	filePathLabel := widget.NewLabel("/home/...")
	filePathLabel.Wrapping = fyne.TextWrapBreak

	var openedContent *fyne.Container
	var startContent *fyne.Container

	editingMsg := container.NewCenter(widget.NewLabel("Annotate in Inkscape.\nOnce done, save and close Inkscape and continue here."))
	savingMsg := container.NewCenter(widget.NewLabel("Saving ..."))
	openingMsg := container.NewCenter(widget.NewLabel("Opening ..."))
	gridScroll := container.NewVScroll(widget.NewLabel(""))

	startContent = container.NewCenter(widget.NewButton("Open PDF File", func() {
		dialog.ShowFileOpen(func(r fyne.URIReadCloser, err error) {

			// Get the file path

			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			if r == nil {
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

			win.SetContent(openingMsg)
			sess, err = session.New(path)
			win.SetContent(startContent)
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
				grid.SetAnnotated(page, sess.IsAnnotated(page))
			}, func(page int) {
				sess.Clear(page)
				grid.SetAnnotated(page, false)
			})

			gridScroll.Content = grid.Root()

			fileNameLabel.SetText("Annotating: " + r.URI().Name())
			filePathLabel.SetText(path)
			win.SetContent(openedContent)

		}, win)
	}))

	closeSession := func() {
		gridScroll.Content = widget.NewLabel("")
		sess.Close()
		sess = nil
		win.SetContent(startContent)
	}

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
						if w == nil {
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
					if sess.HasAnnotations() {
						dialog.ShowConfirm("", "You will lose your annotations if you continue", func(c bool) {
							if c {
								closeSession()
							}
						}, win)
						return
					}
					closeSession()
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
	log.SetPrefix("pdfrankenstein: ")
	flag.Parse()
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
