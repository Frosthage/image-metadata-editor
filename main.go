package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	jpegstructure "github.com/dsoprea/go-jpeg-image-structure/v2"
)

func main() {
	scanLong := flag.Bool("scan", false, "Scan a directory and create bilder.csv")
	scanShort := flag.Bool("s", false, "Alias for --scan")
	applyLong := flag.Bool("apply", false, "Apply titles from bilder.csv in a directory")
	applyShort := flag.Bool("a", false, "Alias for --apply")

	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n  %s --scan <dir>\n  %s --apply <dir>\n\n", os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	doScan := *scanLong || *scanShort
	doApply := *applyLong || *applyShort
	if doScan == doApply {
		flag.Usage()
		os.Exit(2)
	}

	dir := flag.Arg(0)
	if dir == "" {
		flag.Usage()
		os.Exit(2)
	}

	var err error
	if doScan {
		err = scanDirectory(dir)
	} else {
		err = applyTitlesFromCSV(dir)
	}
	if err != nil {
		log.Fatal(err)
	}
}

const csvFilename = "bilder.csv"

func scanDirectory(dir string) error {
	dir = filepath.Clean(dir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read directory: %w", err)
	}

	csvPath := filepath.Join(dir, csvFilename)
	file, err := os.Create(csvPath)
	if err != nil {
		return fmt.Errorf("create csv: %w", err)
	}
	defer file.Close()

	writer := newCSVWriter(file)
	if err := writer.Write([]string{"filename", "title"}); err != nil {
		return fmt.Errorf("write header: %w", err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() || !entry.Type().IsRegular() {
			continue
		}
		name := entry.Name()
		if strings.EqualFold(name, csvFilename) || !isJPEG(name) {
			continue
		}

		title, err := readTitle(filepath.Join(absDir, entry.Name()))
		if err != nil {
			return err
		}

		if err := writer.Write([]string{name, title}); err != nil {
			return fmt.Errorf("write row for %s: %w", name, err)
		}

	}
	if err := writer.Flush(); err != nil {
		return fmt.Errorf("flush csv: %w", err)
	}

	return nil
}

func applyTitlesFromCSV(dir string) error {
	dir = filepath.Clean(dir)

	csvPath := filepath.Join(dir, csvFilename)
	file, err := os.Open(csvPath)
	if err != nil {
		return fmt.Errorf("open csv: %w", err)
	}
	defer file.Close()

	reader := newCSVReader(file)
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read header: %w", err)
	}
	filenameIdx, titleIdx := headerIndex(header, "filename"), headerIndex(header, "title")
	if filenameIdx == -1 || titleIdx == -1 {
		return fmt.Errorf("csv must include filename and title columns")
	}

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read record: %w", err)
		}
		if filenameIdx >= len(record) {
			continue
		}
		filename := strings.TrimSpace(record[filenameIdx])
		if filename == "" {
			continue
		}
		title := ""
		if titleIdx < len(record) {
			title = record[titleIdx]
		}

		path := filename
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		if err := upsertTitle(path, title); err != nil {
			return fmt.Errorf("apply title for %s: %w", filename, err)
		}
	}

	return nil
}

func headerIndex(header []string, name string) int {
	for i, value := range header {
		if strings.EqualFold(strings.TrimSpace(value), name) {
			return i
		}
	}
	return -1
}

func isJPEG(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".jpg" || ext == ".jpeg"
}

type csvWriter struct {
	writer *bufio.Writer
}

func newCSVWriter(w io.Writer) *csvWriter {
	return &csvWriter{writer: bufio.NewWriter(w)}
}

func (w *csvWriter) Write(record []string) error {
	for i, field := range record {
		if i > 0 {
			if err := w.writeString(","); err != nil {
				return err
			}
		}
		if err := w.writeString(encodeCSVField(field)); err != nil {
			return err
		}
	}
	return w.writeString("\n")
}

func (w *csvWriter) Flush() error {
	return w.writer.Flush()
}

func (w *csvWriter) writeString(value string) error {
	_, err := w.writer.WriteString(value)
	return err
}

func encodeCSVField(field string) string {
	needsQuotes := strings.ContainsAny(field, "\",\n\r")
	if !needsQuotes {
		return field
	}
	escaped := strings.ReplaceAll(field, "\"", "\"\"")
	return `"` + escaped + `"`
}

type csvReader struct {
	scanner *bufio.Scanner
}

func newCSVReader(r io.Reader) *csvReader {
	scanner := bufio.NewScanner(r)
	const maxScanTokenSize = 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxScanTokenSize)
	return &csvReader{scanner: scanner}
}

func (r *csvReader) Read() ([]string, error) {
	if !r.scanner.Scan() {
		if err := r.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	line := r.scanner.Text()
	if strings.HasSuffix(line, "\r") {
		line = strings.TrimSuffix(line, "\r")
	}
	record, err := parseCSVLine(line)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func parseCSVLine(line string) ([]string, error) {
	var fields []string
	var field strings.Builder
	inQuotes := false

	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch ch {
		case '"':
			if inQuotes && i+1 < len(line) && line[i+1] == '"' {
				field.WriteByte('"')
				i++
				continue
			}
			inQuotes = !inQuotes
		case ',':
			if inQuotes {
				field.WriteByte(ch)
				continue
			}
			fields = append(fields, field.String())
			field.Reset()
		default:
			field.WriteByte(ch)
		}
	}

	if inQuotes {
		return nil, fmt.Errorf("unterminated quoted field")
	}

	fields = append(fields, field.String())
	return fields, nil
}

func readTitle(path string) (string, error) {
	mp := jpegstructure.NewJpegMediaParser()
	intfc, err := mp.ParseFile(path)
	if err != nil {
		return "", fmt.Errorf("parse JPEG: %w", err)
	}

	sl := intfc.(*jpegstructure.SegmentList)

	rootIfd, _, err := sl.Exif()
	if err != nil {
		return "", fmt.Errorf("parse EXIF: %w", err)
	}

	results, err := rootIfd.FindTagWithName("ImageDescription")
	if err != nil {
		// Tag not found or other error
		return "", nil
	}

	if len(results) == 0 {
		return "", nil
	}

	value, err := results[0].Value()
	if err != nil {
		return "", nil
	}
	switch title := value.(type) {
	case string:
		return title, nil
	case []string:
		if len(title) > 0 {
			return title[0], nil
		}
	case []byte:
		return string(title), nil
	case [][]byte:
		if len(title) > 0 {
			return string(title[0]), nil
		}
	}

	return "", nil
}

func upsertTitle(path, title string) error {
	mp := jpegstructure.NewJpegMediaParser()
	intfc, err := mp.ParseFile(path)
	if err != nil {
		return fmt.Errorf("parse JPEG: %w", err)
	}

	sl := intfc.(*jpegstructure.SegmentList)

	rootIb, err := sl.ConstructExifBuilder()
	if err != nil {
		return fmt.Errorf("build EXIF: %w", err)
	}

	if err := rootIb.SetStandardWithName("ImageDescription", title); err != nil {
		return fmt.Errorf("set title: %w", err)
	}

	if err := sl.SetExif(rootIb); err != nil {
		return fmt.Errorf("write EXIF to JPEG structure: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("open for write: %w", err)
	}
	defer f.Close()

	if err := sl.Write(f); err != nil {
		return fmt.Errorf("write JPEG: %w", err)
	}

	return nil
}
