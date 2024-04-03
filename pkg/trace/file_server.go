package trace

import (
	"io"
	"log"
	"net/http"
	"os"
	"path"
)

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
