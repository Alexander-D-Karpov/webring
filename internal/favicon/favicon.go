package favicon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	htmlTimeout = 5 * time.Second
	dlTimeout   = 10 * time.Second
)

func GetAndStoreFavicon(siteURL, mediaFolder string, siteID int) (string, error) {
	baseURL, err := url.Parse(siteURL)
	if err != nil {
		return "", err
	}

	faviconURL, err := getFaviconFromHTML(baseURL)
	if err == nil {
		faviconPath, err := downloadFavicon(faviconURL, baseURL, mediaFolder, siteID)
		if err == nil {
			return faviconPath, nil
		}
		log.Printf("Failed to download favicon from HTML link: %v", err)
	}

	commonFaviconNames := []string{
		"favicon.ico",
		"favicon.png",
		"favicon.jpg",
		"favicon.svg",
		"favicon.gif",
		"apple-touch-icon.png",
		"apple-touch-icon-precomposed.png",
	}

	for _, name := range commonFaviconNames {
		faviconURL := baseURL.ResolveReference(&url.URL{Path: name})
		faviconPath, err := downloadFavicon(faviconURL, baseURL, mediaFolder, siteID)
		if err == nil {
			return faviconPath, nil
		}
		log.Printf("Failed to download %s: %v", name, err)
	}

	return "", errors.New("failed to find and download favicon")
}

func getFaviconFromHTML(baseURL *url.URL) (*url.URL, error) {
	ctx, cancel := context.WithTimeout(context.Background(), htmlTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", baseURL.String(), http.NoBody)
	if err != nil {
		return nil, err
	}

	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("Failed to close response body: %v", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch HTML: status code %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var faviconURL string
	var exists bool

	doc.Find("link[rel='icon'], link[rel='shortcut icon']").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		faviconURL, exists = s.Attr("href")
		return !exists
	})

	if !exists {
		log.Printf("No favicon link found for site: %s", baseURL.String())
		return nil, errors.New("favicon not found in HTML")
	}

	parsedFaviconURL, err := url.Parse(faviconURL)
	if err != nil {
		return nil, err
	}

	if !parsedFaviconURL.IsAbs() {
		parsedFaviconURL = baseURL.ResolveReference(parsedFaviconURL)
	}

	return parsedFaviconURL, nil
}

func downloadFavicon(faviconURL, baseURL *url.URL, mediaFolder string, siteID int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dlTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", faviconURL.String(), http.NoBody)
	if err != nil {
		return "", err
	}

	userAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Referer", baseURL.String())

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("Failed to close response body: %v", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download favicon: status code %d", resp.StatusCode)
	}

	hasher := sha256.New()
	if _, hashErr := fmt.Fprintf(hasher, "%d-%s", siteID, faviconURL); hashErr != nil {
		return "", hashErr
	}
	hash := hex.EncodeToString(hasher.Sum(nil))

	ext := filepath.Ext(faviconURL.Path)
	if ext == "" {
		ext = ".ico"
	}

	fileName := fmt.Sprintf("favicon-%d-%s%s", siteID, hash[:8], ext)
	filePath := filepath.Join(mediaFolder, fileName)

	out, err := os.Create(filePath)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil {
			log.Printf("Failed to close file: %v", cerr)
		}
	}()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		if removeErr := os.Remove(filePath); removeErr != nil {
			log.Printf("Failed to remove file after copy error: %v", removeErr)
		}
		return "", err
	}

	return fileName, nil
}
