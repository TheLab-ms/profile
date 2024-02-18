package files

import (
	"context"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// NewFileServerHandler returns a simple file server implementation.
// - A JWT is expected in the query param "t".
// - A return URL can be provided in "r".
// - The JWT subject is empty for uploads, set to the ID of a file for downloads.
// - Users are redirected to the provided return URL with the query param "i" containing the ID of the uploaded file
func NewFileServerHandler(signingKey []byte, dir string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.URL.Query().Get("t")
		returnURL := r.URL.Query().Get("r")

		// Authenticate
		claims := &jwt.RegisteredClaims{}
		_, err := jwt.ParseWithClaims(token, claims, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return []byte(signingKey), nil
		})
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			log.Printf("got unauthenticated request to file server: %s", err)
			return
		}
		fileID := claims.Subject

		// Download the file if an ID was given
		if fileID != "" {
			name, err := os.Readlink(path.Join(dir, ".name."+fileID))
			if err != nil {
				log.Printf("couldn't read link: %s", err)
				http.Error(w, "internal error", 500)
				return
			}

			file, err := os.Open(path.Join(dir, fileID))
			if os.IsNotExist(err) {
				http.Error(w, "file doesn't exist", 404)
				return
			}
			if err != nil {
				log.Printf("couldn't open file: %s", err)
				http.Error(w, "internal error", 500)
				return
			}
			defer file.Close()

			w.Header().Add("Content-Type", "application/octet-stream")
			w.Header().Add("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))
			io.Copy(w, file)
			return
		}

		// Generate a unique ID and make sure it doesn't collide with any others
		var filePath string
		for i := 0; i < 9; i++ {
			fileID = newID()
			filePath = path.Join(dir, fileID)
			_, err := os.Stat(filePath)
			if err == nil {
				filePath = ""
				continue // regenerate
			}
		}
		if filePath == "" {
			log.Printf("couldn't generate a unique file ID after 10 attempts")
			http.Error(w, "internal error", 500)
			return
		}

		// Upload
		file, err := os.Create(filePath)
		if err != nil {
			log.Printf("couldn't open file: %s", err)
			http.Error(w, "internal error", 500)
			return
		}
		defer file.Close()

		filereader, header, err := r.FormFile("upload")
		if err != nil {
			http.Error(w, "encoding error", 400)
			return
		}
		const limit = 1024 * 1024 * 1024 * 3 // 3gb limit
		if header.Size > limit {
			http.Error(w, "file is too big", 400)
			return
		}

		err = os.Symlink(header.Filename, path.Join(dir, ".name."+fileID))
		if err != nil {
			log.Printf("couldn't write file name symlink: %s", err)
			http.Error(w, "internal error", 500)
			return
		}

		reader := http.MaxBytesReader(w, filereader, limit)
		defer reader.Close()

		_, err = io.Copy(file, reader)
		if err != nil {
			http.Error(w, "upload error", 400)
			return
		}

		// Send them back!
		url := fmt.Sprintf("%s?i=%s", returnURL, fileID)
		http.Redirect(w, r, url, http.StatusSeeOther)
	})
}

var idRunes = []rune("abcdefghijklmnopqrstuvwxyz1234567890")

func newID() string {
	b := make([]rune, 4)
	for i := range b {
		b[i] = idRunes[rand.Intn(len(idRunes))]
	}
	return string(b)
}

// StartCleanupLoop removes any files that have been in the given dir for longer than the retention.
func StartCleanupLoop(ctx context.Context, dir string, retention time.Duration) {
	err := os.MkdirAll(dir, 0755)
	if err != nil {
		panic(err)
	}
	go func() {
		ticker := time.NewTicker(time.Second * 10)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			files, err := os.ReadDir(dir)
			if err != nil {
				log.Printf("unable to list files: %s", err)
				continue
			}
			for _, file := range files {
				info, err := file.Info()
				if err != nil {
					log.Printf("unable to get file info: %s", err)
					continue
				}
				age := time.Since(info.ModTime())
				if age < retention {
					continue
				}

				err = os.Remove(path.Join(dir, file.Name()))
				if err != nil {
					log.Printf("unable to remove file: %s", err)
					continue
				}
				log.Printf("removed file %q because it's %d seconds old", file.Name(), int(age.Seconds()))
			}
		}
	}()
}
