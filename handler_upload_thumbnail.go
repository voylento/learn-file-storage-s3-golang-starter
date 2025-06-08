package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var allowedTypes = map[string]bool{
    "image/jpeg": true,
    "image/png":  true,
}

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	const maxMemory = 10 << 20
	r.ParseMultipartForm(maxMemory)

	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	// Get original filename and extension
	originalFilename := header.Filename
	fileExtension := filepath.Ext(originalFilename) // ".jpg", ".png", etc.
	
	mediaType := header.Header.Get("Content-Type")
	if mediaType == "" {
		respondWithError(w, http.StatusBadRequest, "Unable to parse Content-Type header", nil)
		return
	}

	if !allowedTypes[mediaType] {
		respondWithError(w, http.StatusBadRequest, "Unsupported file type", nil)
		return
	}

	// Read only the first 512 bytes for content type detection
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		respondWithError(w, http.StatusInternalServerError, "Unable to read file data", err)
		return
	}

	actualType := http.DetectContentType(buffer[:n])
	if !strings.HasPrefix(actualType, "image/") {
		respondWithError(w, http.StatusBadRequest, "File is not an image type", nil)
		return
	}

	// Reset file position to the beginning
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to reset file position", err)
		return
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Video ID", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", err)
		return
	}

	buffName := make([]byte, 32)
	_, err = rand.Read(buffName) 
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create thumbnail name", err)
		return
	}
	newFilename := base64.URLEncoding.EncodeToString(buffName)
	baseURL := fmt.Sprintf("http://localhost:%s", cfg.port)
	assetsDir := strings.TrimPrefix(cfg.assetsRoot, "./")
	filename := fmt.Sprintf("%s%s", newFilename, fileExtension)

	fullURL := baseURL + "/" + path.Join(assetsDir, filename)
	fmt.Printf("fullVideoPath: %s\n", fullURL)
	onDiskFilename := fmt.Sprintf("%s/%s", cfg.assetsRoot, filename)
	if err = saveVideoFile(onDiskFilename, file); err != nil {
		fmt.Printf("Call to saveVideoFile failed: %w\n", err)
		respondWithError(w, http.StatusInternalServerError, "Failed to upload file", err)
		return
	}
	video.ThumbnailURL = &fullURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to save updated thumbnail", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}

func saveVideoFile(filepath string, src multipart.File) error {
	fmt.Println("saveVideoFile")
	dst, err := os.Create(filepath)
	if err != nil {
		fmt.Println("os.Create failed")
		return err
	}
	defer dst.Close()

	_, err = io.Copy(dst, src)

	return err
}
