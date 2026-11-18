package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
)

// errNoUploadedFile says a file field arrived empty. It is distinct from every
// other FormFile failure so a handler can tell "nobody chose a file" from
// "the body was refused", which are different answers to give a producer.
var errNoUploadedFile = errors.New("no file was attached")

// bulkUpload returns the file a form carried under field.
//
// A body that ran into the request size cap fails this same parse. Returning
// that error unchanged is the point: the framework turns it into a 413, which
// tells the producer their roster was too big. Folding it into the missing-file
// answer would send somebody back to a file picker that had been working.
func bulkUpload(r *http.Request, field string) (multipart.File, error) {
	file, _, err := r.FormFile(field)
	if err == nil {
		return file, nil
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return nil, err
	}
	return nil, fmt.Errorf("%s: %w", field, errNoUploadedFile)
}

// csvRows reads CSV into one map per row, keyed by the header row, which is
// what lets a bulk file name its own columns: an awards file with a Slug,
// Title, Subtitle header is the same shape as the form that adds one award by
// hand.
//
// A file whose rows disagree with its header is rejected whole rather than
// imported up to the offending row, because the alternative is a producer who
// believes the ceremony is fully loaded when it stopped at row nine.
func csvRows(reader io.Reader) ([]map[string]string, error) {
	r := csv.NewReader(reader)
	// FieldsPerRecord is relaxed so a trailing newline or a comment line does
	// not read as a short row; the header still decides what each row should
	// contain.
	r.FieldsPerRecord = -1

	header, err := r.Read()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the header row: %w", err)
	}

	var rows []map[string]string
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading row %d: %w", len(rows)+2, err)
		}
		if len(record) != len(header) {
			return nil, fmt.Errorf("row %d has %d values, the header has %d",
				len(rows)+2, len(record), len(header))
		}

		row := make(map[string]string, len(header))
		for i, name := range header {
			row[name] = record[i]
		}
		rows = append(rows, row)
	}
	return rows, nil
}
