package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

func (cfg apiConfig) ensureAssetsDir() error {
	if _, err := os.Stat(cfg.assetsRoot); os.IsNotExist(err) {
		return os.Mkdir(cfg.assetsRoot, 0755)
	}
	return nil
}

func getAssetFilename(mediaType string) string {
	bytes := make([]byte, 16)
	_, err := rand.Read(bytes)
	if err != nil {
		panic("failed to generate random bytes for asset filename")
	}
	
	filename := base64.RawURLEncoding.EncodeToString(bytes)
	ext := mediaTypeExt(mediaType)
	return fmt.Sprintf("%s%s", filename, ext)
}

func mediaTypeExt(mediaType string) string {
	parts := strings.Split(mediaType, "/")
	if len(parts) != 2 {
		return ".bin"
	}

	return "." + parts[1]
}

func (cfg apiConfig) getObjectUrl(key string) string {
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, key)
}

func (cfg apiConfig) getCloudFlareUrl(key string) string {
	return fmt.Sprintf("https://%s/%s", cfg.s3CfDistribution, key)
}
