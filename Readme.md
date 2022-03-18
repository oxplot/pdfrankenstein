PDFrankestein intends to fill the gap on setups (mostly Linux and FOSS)
where a good capable PDF annotator like Adobe Acrobat does not exist.

## What can you do with it?

- Put your signature on documents.
- Fill forms.
- Add clickable links.
- Draw on documents and highlight areas.
- Anything you can do in Inkscape.

## Requirements

You need a recent version of [Inkscape](https://inkscape.org/) and
[qpdf](https://github.com/qpdf/qpdf).

## Download

Download the latest version from the [releases
page](https://github.com/oxplot/pdfrankestein/releases). Alternatively,
you can checkout and build the code:

```sh
git clone https://github.com/oxplot/pdfrankestein.git
cd pdfrankestein
go build
./pdfrankestein
```

## How does it work?

When you select a page to annotate, it's converted to SVG, made into a
locked background of another SVG which is opened in Inkscape for you to
draw on. Once done, the background is removed, the drawings are exported
to a PDF and finally overlayed on top of the original page in the final
PDF.

Inkscape is used much like `vim` or `emacs` are used as your editor in
the shell when you run `crontab -e`. Instead of `crontab` implementing
its own editor, it creates a temporary file, runs `vim` and checks if
the file is updated after `vim` is closed.
