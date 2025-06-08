package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	userID, err := getUserID(w, r, cfg)
	if err != nil {
		return
	}

	video, err := getVideoFromDb(w, r, cfg)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Video ID", err)
		return
	}

	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized", err)
		return
	}

	const maxUploadSize = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	err = r.ParseMultipartForm(maxUploadSize)
	if err != nil {
		respondWithError(w, http.StatusRequestEntityTooLarge, "Video too large", err)
		return
	}

	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Error retrieving video file", err)
		return
	}
	defer file.Close()

	contentType := header.Header.Get("Content-Type")
	mediaType, err := validateVideoFile(file, contentType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid format", err)
		return
	}

	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create video file", err)
		return
	}
	defer tempFile.Close()
	defer os.Remove(tempFile.Name())

	if _, err := io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to write video file", err)
		return
	}

	// Reset file point after copying bytes
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to reset file position", err)
		return
	}

	aspectRatio, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error determining video aspect ratio", err)
		return
	}

	tempFile.Close()

	processedFilename, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error processing file for fast start", err)
		return
	}
	
	processedFile, err := os.Open(processedFilename)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to reopen fast start video file", err)
		return
	}
	defer processedFile.Close()
	defer os.Remove(processedFile.Name())

	orientation := getVideoOrientation(aspectRatio)
	videoFilename := getAssetFilename(mediaType)
	s3Key := filepath.Join(orientation, videoFilename)


	s3ObjInput := s3.PutObjectInput{
		Bucket:		aws.String(cfg.s3Bucket),
		Key:		  aws.String(s3Key),	
		Body: 		processedFile,
		ContentType:	aws.String(mediaType),
	}

	// Verify we are uploading a valid file
	if stat, err := processedFile.Stat(); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Cannot stat processed file", err)
		return
	}

	_, err = cfg.s3Client.PutObject(context.Background(), &s3ObjInput) 
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error uploading video to S3", err)
		return
	}

	objUrl := cfg.getCloudFlareUrl(s3Key)

	video.VideoURL = &objUrl
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to save video url to db", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}

func getUserID(w http.ResponseWriter, r *http.Request, cfg *apiConfig) (uuid.UUID, error) {
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return uuid.UUID{}, err
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return uuid.UUID{}, err
	}

	return userID, nil
}

func getVideoFromDb(w http.ResponseWriter, r *http.Request, cfg *apiConfig) (database.Video, error) {
	videoID, err := uuid.Parse(r.PathValue("videoID"))
	if err != nil {
		return database.Video{}, err
	}

	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		return database.Video{}, err
	}

	return video, nil
}

func validateVideoFile(file multipart.File, contentType string) (string, error) {
	if contentType == "" {
		return "", fmt.Errorf("Invalid Content-Type header")
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("Invalid Content-Type header: %v", err)
	}

	if !strings.HasPrefix(mediaType, "video/") {
		return "", fmt.Errorf("file must be video, got %v", mediaType)
	}

	allowedVideoTypes := map[string]bool{
			"video/mp4":        true,
			"video/mpeg":       false,
			"video/quicktime":  false,
			"video/x-msvideo":  false, // .avi
			"video/webm":       false,
		}

	if !allowedVideoTypes[mediaType] {
		return "", fmt.Errorf("video format not supported: %s", mediaType)
	}

	return mediaType, nil
}

type FFProbeOutput struct {
	Streams	[]struct{
		CodecType	string	`json:"codec_type"`
		Width		int	`json:"width,omitempty"`
		Height	int	`json:"height,omitempty"`
	} `json:"streams"`
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", 
			"-v", "error", 
			"-print_format", "json", 
			"-show_streams", filePath)
	
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe error: %v", err)
	}

	var result FFProbeOutput
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return "", err
	}

	for _, stream := range result.Streams {
		if stream.CodecType == "video" {
			return calculateAspectRatio(stream.Width, stream.Height), nil
		}
	}

	return "", errors.New("Invalid Video Codec")
}

func calculateAspectRatio(width, height int) string {
	if width == 0 || height == 0 {
		return "other"
	}

	ratio := float64(width) / float64(height)
	tolerance := 0.05

	landscapeRatio := 16.0 / 9.0
	if math.Abs(ratio-landscapeRatio)/landscapeRatio <= tolerance {
		return "16:9"
	}

	portraitRatio := 9.0 / 16.0
	if math.Abs(ratio-portraitRatio) / portraitRatio <= tolerance {
		return "9:16"
	}

	return "other"
}

func getVideoOrientation(ar string) string {
	if ar == "16:9" {
		return "landscape"
	}
	if ar == "9:16" {
		return "portrait"
	}
	return "other"
}

func processVideoForFastStart(filename string) (string, error) {
	processedFile, err := os.CreateTemp("", "tubely-processed-*.mp4")
	if err != nil {
		return "", fmt.Errorf("Failed to create temp file for fast start processing: %v", err)
	}

	processedFilename := processedFile.Name()
	processedFile.Close() // Close so ffmpeg can write to it

	// Check input file first
	if stat, err := os.Stat(filename); err != nil {
		os.Remove(processedFilename)
		return "", fmt.Errorf("input file not found: %v", err)
	} else if stat.Size() == 0 {
		os.Remove(processedFilename)
		return "", fmt.Errorf("input file is empty")
	} 

	cmd := exec.Command("ffmpeg", 
		"-y",
		"-i", filename,
		"-c", "copy", 
		"-movflags", "faststart",
		"-f", "mp4",
		processedFilename,
	)

	// Capture both stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		os.Remove(processedFilename)
		return "", fmt.Errorf("ffmpeg error: %v", err)
	}

	// Verify the file was created and has content
	if stat, err := os.Stat(processedFilename); err != nil {
		os.Remove(processedFilename)
		return "", fmt.Errorf("processed file not found: %v", err)
	} else if stat.Size() == 0 {
		os.Remove(processedFilename)
		return "", fmt.Errorf("processed file is empty")
	}

	return processedFilename, nil
}

