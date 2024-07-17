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
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

func GetAndStoreFavicon(siteURL string, mediaFolder string, siteID int) (string, error) {
	faviconURL, err := getFaviconFromHTML(siteURL)
	if err == nil {
		faviconPath, err := downloadFavicon(faviconURL, siteURL, mediaFolder, siteID)
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
		faviconURL := fmt.Sprintf("%s/%s", siteURL, name)
		faviconPath, err := downloadFavicon(faviconURL, siteURL, mediaFolder, siteID)
		if err == nil {
			return faviconPath, nil
		}
		log.Printf("Failed to download %s: %v", name, err)
	}

	return "", errors.New("failed to find and download favicon")
}

func getFaviconFromHTML(siteURL string) (string, error) {
	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	req, err := http.NewRequest("GET", siteURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

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
		return "", fmt.Errorf("failed to fetch HTML: status code %d", resp.StatusCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return "", err
	}

	var faviconURL string
	var exists bool

	doc.Find("link[rel='icon'], link[rel='shortcut icon']").EachWithBreak(func(i int, s *goquery.Selection) bool {
		faviconURL, exists = s.Attr("href")
		return !exists // break if we found a favicon
	})

	if !exists {
		log.Printf("No favicon link found for site: %s", siteURL)
		return "", errors.New("favicon not found in HTML")
	}

	if !strings.HasPrefix(faviconURL, "http") {
		baseURL, err := url.Parse(siteURL)
		if err != nil {
			return "", err
		}
		faviconURL = baseURL.ResolveReference(&url.URL{Path: faviconURL}).String()
	}

	return faviconURL, nil
}

func downloadFavicon(faviconURL, siteURL, mediaFolder string, siteID int) (string, error) {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	req, err := http.NewRequest("GET", faviconURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Referer", siteURL)

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
