package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"

	"github.com/oxplot/pdfrankenstein/session"
)

const (
	progName = "PDFrankenstein"
)

var (
	cleanCSS *gtk.CssProvider
	dirtyCSS *gtk.CssProvider
	//go:embed splash.svg
	splash []byte
	//go:embed icon.svg
	appIcon []byte
	//go:embed loading.svg
	loadingImgBytes []byte
	loadingPix      *gdk.Pixbuf
	//go:embed nothumb.svg
	noThumbImgBytes []byte
	noThumbPix      *gdk.Pixbuf
)

func shrinkHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	dir, file := filepath.Split(path)
	if strings.HasPrefix(dir, home) {
		return filepath.Join("~"+dir[len(home):], file)
	}
	return path
}

var (
	annotSinceLastSave bool

	sessMu     sync.Mutex
	sess       *session.Session
	cancelLoad func()

	mainWin   *gtk.Window
	mainStack *gtk.Stack
	openBut   *gtk.Button
	saveBut   *gtk.Button
	closeBut  *gtk.Button
	hdrBar    *gtk.HeaderBar
	pageFlow  *gtk.FlowBox

	pageImages []*gtk.Image
	pageLabels []*gtk.Label
)

func showErrMsg(title string, msg string) {
	d, err := gtk.DialogNew()
	if err != nil {
		log.Fatalf("unable to create dialog: %s", err)
	}
	defer d.Destroy()
	d.SetTitle(title)
	d.SetModal(true)
	d.SetTransientFor(mainWin)

	b, err := d.AddButton("Close", gtk.RESPONSE_OK)
	if err != nil {
		log.Fatalf("unable to create dialog button: %s", err)
	}
	b.SetMarginTop(10)
	b.SetMarginBottom(10)
	b.SetMarginStart(10)
	b.SetMarginEnd(10)

	l, err := gtk.LabelNew(msg)
	if err != nil {
		log.Fatalf("unable to create dialog label: %s", err)
	}
	l.SetMarginTop(10)
	l.SetMarginBottom(10)
	l.SetMarginStart(10)
	l.SetMarginEnd(10)
	con, err := d.GetContentArea()
	if err != nil {
		log.Fatalf("unable to get dialog content area: %s", err)
	}
	con.Add(l)

	d.ShowAll()
	_ = d.Run()
	d.Close()
}

func loadThumbs(ctx context.Context, loadThumb func(p int) (string, error)) {
	cnt := len(pageImages)
	for i := 0; i < cnt; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}
		path, err := loadThumb(i)
		if err != nil {
			log.Printf("failed to load thumbnail: %s", err)
			func(img *gtk.Image, path string) {
				glib.IdleAdd(func() {
					img.SetFromPixbuf(noThumbPix)
				})
			}(pageImages[i], path)
		} else {
			func(img *gtk.Image, path string) {
				glib.IdleAdd(func() {
					img.SetFromFile(path)
				})
			}(pageImages[i], path)
		}
	}
}

func addCSS(w gtk.IWidget, css *gtk.CssProvider) {
	ctx, err := w.ToWidget().GetStyleContext()
	if err != nil {
		log.Fatalf("unable to get style context: %s", err)
	}
	ctx.AddProvider(css, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
}

func removeCSS(w gtk.IWidget, css *gtk.CssProvider) {
	ctx, err := w.ToWidget().GetStyleContext()
	if err != nil {
		log.Fatalf("unable to get style context: %s", err)
	}
	ctx.RemoveProvider(css)
}

func clearAnnotation(page int) {
	d, err := gtk.DialogNewWithButtons("Clear page annotations?", mainWin, gtk.DIALOG_MODAL,
		[]any{"Clear", gtk.RESPONSE_OK},
		[]any{"Keep", gtk.RESPONSE_CANCEL})
	if err != nil {
		log.Fatalf("unable to create confirmation dialog: %s", err)
	}
	if d.Run() == gtk.RESPONSE_OK {
		sessMu.Lock()
		sess.Clear(page)
		sessMu.Unlock()
		pageLabels[page].SetText(strconv.Itoa(page + 1))
		removeCSS(pageLabels[page], dirtyCSS)
	}
	d.Close()
	d.Destroy()
}

func open(path string) {
	var err error

	if path == "" {
		ofd, err := gtk.FileChooserDialogNewWith1Button(
			"Open PDF File",
			mainWin,
			gtk.FILE_CHOOSER_ACTION_OPEN,
			"Open",
			gtk.RESPONSE_OK,
		)
		if err != nil {
			log.Fatalf("failed to open file chooser: %s", err)
		}
		defer ofd.Destroy()
		filter, err := gtk.FileFilterNew()
		if err != nil {
			log.Fatalf("failed to create file filter: %s", err)
		}
		filter.AddMimeType("application/pdf")
		filter.SetName("PDF Document")
		ofd.SetLocalOnly(true)
		ofd.AddFilter(filter)
		if ofd.Run() != gtk.RESPONSE_OK {
			return
		}
		path = ofd.GetFilename()
		ofd.Close()
	}

	mainWin.SetSensitive(false)
	defer mainWin.SetSensitive(true)

	sessMu.Lock()
	sess, err = session.New(path)
	sessMu.Unlock()
	if err != nil {
		log.Printf("failed to open '%s': %s", path, err)
		glib.IdleAdd(func() { showErrMsg("Cannot load file", err.Error()) })
		return
	}

	dir, file := filepath.Split(shrinkHome(path))
	hdrBar.SetTitle(file)
	hdrBar.SetSubtitle(dir)
	openBut.Hide()
	saveBut.Show()
	closeBut.Show()

	// Populate the UI with pages

	pageImages = make([]*gtk.Image, sess.PageCount())
	pageLabels = make([]*gtk.Label, sess.PageCount())
	for i := range pageImages {
		o, err := gtk.OverlayNew()
		if err != nil {
			log.Fatal("Unable to create overlay")
		}

		// Page thumb

		img, err := gtk.ImageNewFromPixbuf(loadingPix)
		if err != nil {
			log.Fatalf("failed to create image asset: %s", err)
		}
		img.Show()
		pageImages[i] = img
		eb, err := gtk.EventBoxNew()
		if err != nil {
			log.Fatalf("unable to create event box: %s", err)
		}
		eb.SetHAlign(gtk.ALIGN_START)
		eb.Add(img)
		eb.AddEvents(int(gdk.BUTTON_PRESS_MASK))
		func(page int) {
			eb.Connect("button-press-event", func() {
				annotate(page)
			})
		}(i)
		eb.Show()
		o.Add(eb)

		// Page Label

		l, err := gtk.LabelNew(strconv.Itoa(i + 1))
		if err != nil {
			log.Fatalf("unable to create label: %s", err)
		}
		addCSS(l, cleanCSS)
		pageLabels[i] = l
		l.Show()

		eb, err = gtk.EventBoxNew()
		if err != nil {
			log.Fatalf("unable to create event box: %s", err)
		}
		eb.Show()
		eb.SetHAlign(gtk.ALIGN_START)
		eb.SetVAlign(gtk.ALIGN_END)
		eb.SetMarginBottom(3)
		eb.SetMarginStart(3)
		eb.Add(l)
		eb.AddEvents(int(gdk.BUTTON_PRESS_MASK))
		func(page int) {
			eb.Connect("button-press-event", func() {
				sessMu.Lock()
				annotated := sess.IsAnnotated(page)
				sessMu.Unlock()
				if annotated {
					clearAnnotation(page)
				}
			})
		}(i)

		o.AddOverlay(eb)

		o.Show()
		pageFlow.Add(o)
	}

	var ctx context.Context
	ctx, cancelLoad = context.WithCancel(context.Background())
	go loadThumbs(ctx, func(p int) (string, error) {
		sessMu.Lock()
		defer sessMu.Unlock()
		if sess == nil || sess.IsClosed() {
			return "", errors.New("session is nil/closed")
		}
		return sess.Thumbnail(p)
	})

	mainStack.SetVisibleChildName("pages")
}

func annotate(page int) {
	mainWin.SetSensitive(false)
	mainStack.SetVisibleChildName("continue-in-inkscape")

	go func() {
		sessMu.Lock()
		changed, err := sess.Annotate(page)
		sessMu.Unlock()

		glib.IdleAdd(func() {
			mainWin.SetSensitive(true)
			mainStack.SetVisibleChildName("pages")
			if err != nil {
				showErrMsg("Cannot annotate file", err.Error())
				return
			}
			if changed {
				annotSinceLastSave = true
				pageLabels[page].SetText(fmt.Sprintf("%d : clear", page+1))
				addCSS(pageLabels[page], dirtyCSS)
			}
		})
	}()
}

func closeFile() bool {
	sessMu.Lock()
	if sess == nil || sess.IsClosed() {
		sessMu.Unlock()
		return true
	}
	sessMu.Unlock()

	if annotSinceLastSave {
		d, err := gtk.DialogNewWithButtons("Your changes will be lost!", mainWin, gtk.DIALOG_MODAL,
			[]any{"Close anyway", gtk.RESPONSE_OK},
			[]any{"Keep editing", gtk.RESPONSE_CANCEL})
		if err != nil {
			log.Fatalf("unable to create confirmation dialog: %s", err)
		}
		defer d.Destroy()
		defer d.Close()
		if d.Run() != gtk.RESPONSE_OK {
			return false
		}
	}

	pageFlow.GetChildren().Foreach(func(i any) {
		if c, ok := i.(gtk.IWidget); ok {
			pageFlow.Remove(c)
		}
	})

	annotSinceLastSave = false
	resetUIToStart()
	sessMu.Lock()
	if sess != nil {
		cancelLoad()
		sess.Close()
	}
	sessMu.Unlock()

	return true
}

func resetUIToStart() {
	hdrBar.SetTitle("")
	hdrBar.SetSubtitle("")
	openBut.Show()
	saveBut.Hide()
	closeBut.Hide()
	mainStack.SetVisibleChildName("splash")
}

func save() {
	ofd, err := gtk.FileChooserDialogNewWith1Button(
		"Save",
		mainWin,
		gtk.FILE_CHOOSER_ACTION_SAVE,
		"Save",
		gtk.RESPONSE_OK,
	)
	if err != nil {
		log.Fatalf("failed to open file chooser: %s", err)
	}
	defer ofd.Destroy()
	ofd.SetLocalOnly(true)

	filter, err := gtk.FileFilterNew()
	if err != nil {
		log.Fatalf("failed to create file filter: %s", err)
	}
	filter.SetName("PDF documents")
	filter.AddPattern("*.pdf")
	filter.AddPattern("*.PDF")
	ofd.AddFilter(filter)

	if ofd.Run() != gtk.RESPONSE_OK {
		return
	}
	path := ofd.GetFilename()
	ofd.Close()

	if !strings.HasSuffix(strings.ToLower(path), ".pdf") {
		path += ".pdf"
	}

	sessMu.Lock()
	err = sess.Save(path)
	sessMu.Unlock()
	if err != nil {
		glib.IdleAdd(func() { showErrMsg("Cannot save file", err.Error()) })
		return
	}
	annotSinceLastSave = false
}

func initUI() error {
	var err error

	gtk.Init(nil)

	// Assets

	loadingPix, err = gdk.PixbufNewFromBytesOnly(loadingImgBytes)
	if err != nil {
		return fmt.Errorf("failed to create loading pixbuf: %s", err)
	}
	noThumbPix, err = gdk.PixbufNewFromBytesOnly(noThumbImgBytes)
	if err != nil {
		return fmt.Errorf("failed to create no thumb pixbuf: %s", err)
	}

	cleanCSS, err = gtk.CssProviderNew()
	if err != nil {
		return fmt.Errorf("failed to create css provider: %s", err)
	}
	cleanCSS.LoadFromData(
		`label{border-radius:3px;padding:2px 6px;background:@theme_bg_color;opacity:0.8}`)
	dirtyCSS, err = gtk.CssProviderNew()
	if err != nil {
		return fmt.Errorf("failed to create css provider: %s", err)
	}
	dirtyCSS.LoadFromData(`label{color:white;background:orange;opacity:1}`)

	// Main window

	mainWin, err = gtk.WindowNew(gtk.WINDOW_TOPLEVEL)
	if err != nil {
		return fmt.Errorf("failed to create main window: %s", err)
	}
	mainWin.Connect("delete-event", func() bool {
		return !closeFile()
	})
	mainWin.Connect("destroy", func() {
		gtk.MainQuit()
	})

	dragTarget, err := gtk.TargetEntryNew("text/uri-list", gtk.TARGET_OTHER_APP, 0)
	if err != nil {
		return fmt.Errorf("failed to create drag target: %s", err)
	}
	mainWin.DragDestSet(gtk.DEST_DEFAULT_ALL, []gtk.TargetEntry{*dragTarget}, gdk.ACTION_COPY)
	mainWin.Connect("drag-data-received", func(_ *gtk.Window, _ *gdk.DragContext, x, y int, s *gtk.SelectionData, m int, t uint) {
		uri := strings.SplitN(string(s.GetData()), "\r", 2)[0]
		if !strings.HasPrefix(uri, "file://") {
			return
		}
		if closeFile() {
			open(strings.TrimPrefix(uri, "file://"))
		}
	})

	iconPix, err := gdk.PixbufNewFromBytesOnly(appIcon)
	if err != nil {
		return fmt.Errorf("failed to create main icon pixbuf: %s", err)
	}
	mainWin.SetIcon(iconPix)
	mainWin.Iconify()
	mainWin.SetDefaultSize(640, 400)

	hdrBar, err = gtk.HeaderBarNew()
	if err != nil {
		return fmt.Errorf("failed to create main header bar: %s", err)
	}
	hdrBar.SetDecorationLayout("icon:menu,minimize,close")
	hdrBar.SetShowCloseButton(true)
	mainWin.SetTitlebar(hdrBar)

	mainStack, err = gtk.StackNew()
	if err != nil {
		return fmt.Errorf("failed to create main stack: %s", err)
	}
	mainWin.Add(mainStack)

	// Main buttons

	saveBut, err = gtk.ButtonNewWithLabel("Save")
	if err != nil {
		return fmt.Errorf("failed to create save button: %s", err)
	}
	saveBut.Connect("clicked", func() { save() })
	closeBut, err = gtk.ButtonNewWithLabel("Close")
	if err != nil {
		return fmt.Errorf("failed to create close button: %s", err)
	}
	closeBut.Connect("clicked", func() { closeFile() })
	openBut, err = gtk.ButtonNewWithLabel("Open PDF File")
	if err != nil {
		return fmt.Errorf("failed to create open button: %s", err)
	}
	openBut.Connect("clicked", func() { open("") })

	hdrBar.Add(openBut)
	hdrBar.Add(saveBut)
	hdrBar.PackEnd(closeBut)

	// Add splash

	splashPix, err := gdk.PixbufNewFromBytesOnly(splash)
	if err != nil {
		return fmt.Errorf("failed to create pixbuf for splash: %s", err)
	}
	splashImg, err := gtk.ImageNewFromPixbuf(splashPix)
	if err != nil {
		return fmt.Errorf("failed to create image for splash: %s", err)
	}
	mainStack.AddNamed(splashImg, "splash")
	mainStack.SetVisibleChildName("splash")

	// Add continue in inkscape message

	l, err := gtk.LabelNew("Continue in Inkscape.\nOnce done, save, close and return here.")
	if err != nil {
		return fmt.Errorf("unable to create label: %s", err)
	}
	mainStack.AddNamed(l, "continue-in-inkscape")

	// Add page flow

	pageFlow, err = gtk.FlowBoxNew()
	if err != nil {
		return fmt.Errorf("failed to create flowbox: %s", err)
	}
	pageFlow.SetSelectionMode(gtk.SELECTION_NONE)
	pageFlow.SetMarginTop(10)
	pageFlow.SetMarginBottom(10)
	pageFlow.SetMarginStart(10)
	pageFlow.SetMarginEnd(10)

	scr, err := gtk.ScrolledWindowNew(nil, nil)
	if err != nil {
		return fmt.Errorf("failed to create scrolled window: %s", err)
	}
	scr.Add(pageFlow)
	mainStack.AddNamed(scr, "pages")

	mainWin.ShowAll()
	resetUIToStart()

	return nil
}

func run() error {
	if err := initUI(); err != nil {
		return fmt.Errorf("failed to initialize UI: %s", err)
	}
	if len(os.Args) > 1 {
		glib.IdleAdd(func() { open(os.Args[1]) })
	}
	gtk.Main()
	return nil
}

func main() {
	log.SetFlags(0)
	log.SetPrefix(strings.ToLower(progName) + ": ")
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
