package crawl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ledongthuc/pdf"
)

type pdfReaderAt struct {
	ctx context.Context
	r   *bytes.Reader
}

func (r pdfReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.ReadAt(p, off)
}

func extractPDF(ctx context.Context, raw []byte) (text string, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text, err = "", errors.New("invalid or unsupported PDF structure")
		}
		if ctx.Err() != nil {
			text, err = "", ctx.Err()
		}
	}()
	if len(raw) > maxRawDocBytes {
		return "", errDocumentTooLarge
	}
	reader, err := pdf.NewReader(pdfReaderAt{ctx, bytes.NewReader(raw)}, int64(len(raw)))
	if err != nil {
		return "", fmt.Errorf("PDF extraction: %w", err)
	}
	pages := reader.NumPage()
	if pages > 2000 {
		return "", errors.New("PDF exceeds 2000-page limit")
	}
	var out strings.Builder
	for i := 1; i <= pages; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		items := reader.Page(i).Content().Text
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].Y != items[j].Y {
				return items[i].Y > items[j].Y
			}
			return items[i].X < items[j].X
		})
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&out, "\n## PDF page %d\n\n", i)
		previous := items[0]
		for j, item := range items {
			if j > 0 {
				if item.Y != previous.Y {
					out.WriteByte('\n')
				} else if item.X > previous.X+previous.W+1 {
					out.WriteByte(' ')
				}
			}
			if out.Len()+len(item.S) > maxExtractBytes {
				return "", errDocumentTooLarge
			}
			out.WriteString(item.S)
			previous = item
		}
		out.WriteByte('\n')
	}
	if strings.TrimSpace(out.String()) == "" {
		return "", errors.New("PDF has no extractable text (scanned PDFs require OCR)")
	}
	return strings.TrimSpace(out.String()), nil
}
