package favicon

import (
	"crypto/md5"
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

func GetAndStoreFavicon(siteURL string, mediaFolder string, siteID int) (string, error) {
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
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", baseURL.String(), nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Printf("Failed to close response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch HTML: status code %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, err
	}

	var faviconURL string
	var exists bool

	doc.Find("link[rel='icon'], link[rel='shortcut icon']").EachWithBreak(func(i int, s *goquery.Selection) bool {
		faviconURL, exists = s.Attr("href")
		return !exists // break if we found a favicon
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

func downloadFavicon(faviconURL *url.URL, baseURL *url.URL, mediaFolder string, siteID int) (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", faviconURL.String(), nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Referer", baseURL.String())

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Printf("Failed to close response body: %v", err)
		}
	}(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download favicon: status code %d", resp.StatusCode)
	}

	hasher := md5.New()
	hasher.Write([]byte(fmt.Sprintf("%d-%s", siteID, faviconURL)))
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
	defer func(out *os.File) {
		err := out.Close()
		if err != nil {
			log.Printf("Failed to close file: %v", err)
		}
	}(out)

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		err := os.Remove(filePath)
		if err != nil {
			return "", err
		}
		return "", err
	}

	return fileName, nil
}
