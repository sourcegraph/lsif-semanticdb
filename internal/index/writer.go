package index

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/sourcegraph/lsif-protocol/writer"
)

type jsonWriter struct {
	bufferedWriter *bufio.Writer
	encoder        *json.Encoder
	err            error
}

var _ writer.JSONWriter = &jsonWriter{}

// writerBufferSize is the size of the buffered writer wrapping output to the target file.
const writerBufferSize = 4096

// NewJSONWriter creates a new JSONWriter wrapping the given writer.
func NewJSONWriter(w io.Writer) writer.JSONWriter {
	bufferedWriter := bufio.NewWriterSize(w, writerBufferSize)

	return &jsonWriter{
		bufferedWriter: bufferedWriter,
		encoder:        json.NewEncoder(bufferedWriter),
	}
}

// Write emits a single vertex or edge value.
func (jw *jsonWriter) Write(v interface{}) {
	if err := jw.encoder.Encode(v); err != nil {
		jw.err = err
	}
}

// Flush ensures that all elements have been written to the underlying writer.
func (jw *jsonWriter) Flush() error {
	if jw.err != nil {
		return jw.err
	}

	if err := jw.bufferedWriter.Flush(); err != nil {
		return err
	}

	return nil
}
