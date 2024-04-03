package trace

import (
	"bytes"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path"
)

// UploadFile uploads a file to the given URL. This function does not close the
// file.
func UploadFile(url, chainID, nodeID, table string, file *os.File) error {
	// Prepare a form that you will submit to that URL
	var requestBody bytes.Buffer
	multipartWriter := multipart.NewWriter(&requestBody)
	fileWriter, err := multipartWriter.CreateFormFile("file", file.Name())
	if err != nil {
		return err
	}

	if err := multipartWriter.WriteField("chain_id", chainID); err != nil {
		return err
	}

	if err := multipartWriter.WriteField("node_id", nodeID); err != nil {
		return err
	}

	if err := multipartWriter.WriteField("table", table); err != nil {
		return err
	}

	// Copy the file data to the multipart writer
	if _, err := io.Copy(fileWriter, file); err != nil {
		return err
	}
	multipartWriter.Close()

	// Create a new request to the given URL
	request, err := http.NewRequest("POST", url, &requestBody)
	if err != nil {
		return err
	}

	// Set the content type, this will contain the boundary.
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())

	// Do the request
	client := &http.Client{}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// Check the response
	if response.StatusCode != http.StatusOK {
		return io.ErrUnexpectedEOF
	}

	return nil
}

func uploadHandler(dir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Limit your server to only accept POST requests for file uploads
		if r.Method != http.MethodPost {
			http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
			return
		}

		// Parse the multipart form, with a maxMemory of 10MB. Beyond this limit, files will be written to temp files
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "Error parsing multipart form", http.StatusBadRequest)
			return
		}

		// Get the file from the request. "file" corresponds to the name attribute in your HTML form
		file, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "Error retrieving the file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		chainID := r.FormValue("chain_id")
		nodeID := r.FormValue("node_id")
		table := r.FormValue("table")

		p := path.Join(dir, chainID, nodeID)
		err = os.MkdirAll(p, os.ModePerm)
		if err != nil {
			http.Error(w, "Error creating the directory", http.StatusInternalServerError)
			return
		}

		dst, err := os.Create(path.Join(p, table+".jsonl"))
		if err != nil {
			http.Error(w, "Error creating a file", http.StatusInternalServerError)
			return
		}

		// Copy the file data to our new file
		_, err = io.Copy(dst, file)
		if err != nil {
			http.Error(w, "Error saving the file", http.StatusInternalServerError)
			dst.Close()
			return
		}
		dst.Close()

		// Respond to the client
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("File uploaded successfully"))
	}
}

func StartServer(dir string) {
	http.HandleFunc("/upload", uploadHandler(dir))
	log.Println("Server started on :42042")
	if err := http.ListenAndServe(":42042", nil); err != nil {
		log.Fatalf("Error starting server: %v", err)
	}
}
