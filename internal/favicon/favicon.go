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
	"strings"
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

	rootURL := &url.URL{
		Scheme: baseURL.Scheme,
		Host:   baseURL.Host,
	}

	faviconURL, err := getFaviconFromHTML(baseURL)
	if err == nil {
		faviconPath, dlErr := downloadFavicon(faviconURL, baseURL, mediaFolder, siteID)
		if dlErr == nil {
			return faviconPath, nil
		}
		log.Printf("Failed to download favicon from HTML link: %v", dlErr)
	}

	if baseURL.Path != "" && baseURL.Path != "/" {
		faviconURL, err = getFaviconFromHTML(rootURL)
		if err == nil {
			faviconPath, dlErr := downloadFavicon(faviconURL, rootURL, mediaFolder, siteID)
			if dlErr == nil {
				return faviconPath, nil
			}
			log.Printf("Failed to download favicon from root HTML link: %v", dlErr)
		}
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
		faviconPath, dlErr := downloadFavicon(faviconURL, baseURL, mediaFolder, siteID)
		if dlErr == nil {
			return faviconPath, nil
		}
		log.Printf("Failed to download %s from base path: %v", name, dlErr)
	}

	if baseURL.Path != "" && baseURL.Path != "/" {
		for _, name := range commonFaviconNames {
			faviconURL := rootURL.ResolveReference(&url.URL{Path: "/" + name})
			faviconPath, dlErr := downloadFavicon(faviconURL, rootURL, mediaFolder, siteID)
			if dlErr == nil {
				return faviconPath, nil
			}
			log.Printf("Failed to download %s from root: %v", name, dlErr)
		}
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

	selectors := []string{
		"link[rel='icon']",
		"link[rel='shortcut icon']",
		"link[rel='apple-touch-icon']",
		"link[rel='apple-touch-icon-precomposed']",
	}

	for _, selector := range selectors {
		doc.Find(selector).EachWithBreak(func(_ int, s *goquery.Selection) bool {
			faviconURL, exists = s.Attr("href")
			return !exists
		})
		if exists {
			break
		}
	}

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

func safeJoinUnder(base, name string) (string, error) {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	candAbs, err := filepath.Abs(filepath.Join(base, name))
	if err != nil {
		return "", err
	}
	if candAbs != baseAbs && !strings.HasPrefix(candAbs, baseAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid path: %s", candAbs)
	}
	return candAbs, nil
}

func downloadFavicon(faviconURL, baseURL *url.URL, mediaFolder string, siteID int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dlTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", faviconURL.String(), http.NoBody)
	if err != nil {
		return "", err
	}

	ua := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/91.0.4472.124 Safari/537.36"
	req.Header.Set("User-Agent", ua)
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

	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !isImageContentType(contentType) {
		return "", fmt.Errorf("invalid content type: %s", contentType)
	}

	hasher := sha256.New()
	if _, hashErr := fmt.Fprintf(hasher, "%d-%s", siteID, faviconURL); hashErr != nil {
		return "", hashErr
	}
	hash := hex.EncodeToString(hasher.Sum(nil))

	ext := filepath.Ext(faviconURL.Path)
	if ext == "" || len(ext) > 5 {
		ext = extFromContentType(contentType)
	}

	fileName := fmt.Sprintf("favicon-%d-%s%s", siteID, hash[:8], ext)
	absPath, err := safeJoinUnder(mediaFolder, fileName)
	if err != nil {
		return "", err
	}

	if mkErr := os.MkdirAll(filepath.Dir(absPath), 0o750); mkErr != nil {
		return "", mkErr
	}

	out, err := os.OpenFile(absPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil {
			log.Printf("Failed to close file: %v", cerr)
		}
	}()

	if _, err = io.Copy(out, resp.Body); err != nil {
		if rmErr := os.Remove(absPath); rmErr != nil {
			log.Printf("Failed to remove partial file %q: %v", absPath, rmErr)
		}
		return "", err
	}

	return fileName, nil
}

func isImageContentType(contentType string) bool {
	contentType = strings.ToLower(strings.Split(contentType, ";")[0])
	validTypes := []string{
		"image/",
		"application/octet-stream",
		"image/x-icon",
		"image/vnd.microsoft.icon",
	}
	for _, valid := range validTypes {
		if strings.HasPrefix(contentType, valid) || contentType == valid {
			return true
		}
	}
	return false
}

func extFromContentType(contentType string) string {
	contentType = strings.ToLower(strings.Split(contentType, ";")[0])
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/svg+xml":
		return ".svg"
	case "image/webp":
		return ".webp"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico"
	default:
		return ".ico"
	}
}
