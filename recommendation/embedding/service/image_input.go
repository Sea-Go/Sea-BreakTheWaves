package service

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const localImageFetchTimeout = 20 * time.Second

func normalizeImageInput(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.HasPrefix(raw, "data:") {
		return raw, nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return raw, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return raw, nil
	}
	if !shouldInlineImageHost(parsed.Hostname()) {
		return raw, nil
	}

	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		return "", fmt.Errorf("create image download request failed: %w", err)
	}

	client := &http.Client{Timeout: localImageFetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download local image failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download local image failed: status=%d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read local image failed: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("local image is empty")
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(data)), nil
}

func normalizeImageInputs(urls []string) ([]string, error) {
	normalized := make([]string, 0, len(urls))
	for _, raw := range urls {
		value, err := normalizeImageInput(raw)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func shouldInlineImageHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
	}

	if !strings.Contains(host, ".") {
		return true
	}
	return false
}
