package trace

import (
	"bufio"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
)

func (lc *LocalClient) getTableHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Parse the request to get the data
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Failed to parse form", http.StatusBadRequest)
			return
		}

		inputString := r.FormValue("table")
		if inputString == "" {
			http.Error(w, "No data provided", http.StatusBadRequest)
			return
		}

		f, err := lc.ReadTable(inputString)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to read table: %v", err), http.StatusInternalServerError)
			return
		}

		// Use the pump function to continuously read from the file and write to
		// the response writer
		reader, writer := pump(inputString, bufio.NewReader(f))
		defer reader.Close()

		// Set the content type to the writer's form data content type
		w.Header().Set("Content-Type", writer.FormDataContentType())

		// Copy the data from the reader to the response writer
		if _, err := io.Copy(w, reader); err != nil {
			http.Error(w, "Failed to send data", http.StatusInternalServerError)
			return
		}
	}
}

// pump continuously reads from a bufio.Reader and writes to a multipart.Writer.
// It returns the reader end of the pipe and the writer for consumption by the
// server.
func pump(table string, br *bufio.Reader) (*io.PipeReader, *multipart.Writer) {
	r, w := io.Pipe()
	m := multipart.NewWriter(w)

	go func(
		tableq string,
		m *multipart.Writer,
		w *io.PipeWriter,
		br *bufio.Reader,
	) {
		defer w.Close()
		defer m.Close()

		part, err := m.CreateFormFile("filename", table+".jsonl")
		if err != nil {
			return
		}

		if _, err = io.Copy(part, br); err != nil {
			return
		}

	}(table, m, w, br)

	return r, m
}

func (lc *LocalClient) servePullData() {
	http.HandleFunc("/get_table", lc.getTableHandler())
	err := http.ListenAndServe(":42042", nil) //nolint:gosec
	if err != nil {
		lc.logger.Error("trace pull server failure", "err", err)
	}
}

// GetTable downloads a table from the server and saves it to the given directory. It uses a multipart
// response to download the file.
func GetTable(serverURL, table, dirPath string) error {
	data := url.Values{}
	data.Set("table", table)

	resp, err := http.PostForm(serverURL, data) //nolint:gosec
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return err
	}

	boundary, ok := params["boundary"]
	if !ok {
		panic("Not a multipart response")
	}

	err = os.MkdirAll(dirPath, 0755)
	if err != nil {
		return err
	}

	outputFile, err := os.Create(path.Join(dirPath, table+".jsonl"))
	if err != nil {
		return err
	}
	defer outputFile.Close()

	reader := multipart.NewReader(resp.Body, boundary)

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break // End of multipart
		}
		if err != nil {
			return err
		}

		contentDisposition, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil {
			return err
		}

		if contentDisposition == "form-data" && params["filename"] != "" {
			_, err = io.Copy(outputFile, part)
			if err != nil {
				return err
			}
		}

		part.Close()
	}

	return nil
}
