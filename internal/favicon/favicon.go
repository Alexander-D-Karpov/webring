package favicon

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

type Manifest struct {
	Icons []struct {
		Src string `json:"src"`
	} `json:"icons"`
}

func GetAndStoreFavicon(siteURL, mediaFolder string, siteID int) (string, error) {
	faviconURL := fmt.Sprintf("%s/favicon.ico", siteURL)

	faviconPath, err := downloadFavicon(faviconURL, mediaFolder, siteID)
	if err != nil {
		return "", err
	}

	return faviconPath, nil
}

func downloadFavicon(faviconURL, mediaFolder string, siteID int) (string, error) {
	resp, err := http.Get(faviconURL)
	if err != nil {
		return "", err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			fmt.Printf("Error closing response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", errors.New("failed to download favicon")
	}

	hasher := md5.New()
	hasher.Write([]byte(fmt.Sprintf("%d-%s", siteID, faviconURL)))
	hash := hex.EncodeToString(hasher.Sum(nil))

	ext := filepath.Ext(faviconURL)
	if ext == "" {
		ext = ".ico"
	}

	fileName := fmt.Sprintf("favicon-%d-%s%s", siteID, hash[:8], ext)
	filePath := filepath.Join(mediaFolder, fileName)

	out, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer func(out *os.File) {
		err := out.Close()
		if err != nil {
			fmt.Printf("Error closing file: %v", err)
		}
	}(out)

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		return "", err
	}

	return fileName, nil
}
