package confluence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"git.kuainiujinke.com/argus/ai-sdlc-factory/internal/domain"
	"golang.org/x/net/html"
)

var (
	pageURLPattern = regexp.MustCompile(`https://[A-Za-z0-9.-]+/wiki/(?:spaces/[^/\s]+/pages/|pages/viewpage\.action\?pageId=)([0-9]+)[^\s<)]*`)
	pageIDPattern  = regexp.MustCompile(`/pages/([0-9]+)(?:/|$)`)
)

type Client struct {
	baseURL string
	email   string
	token   string
	http    *http.Client
}

func New(baseURL, email, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		email:   email,
		token:   token,
		http:    &http.Client{Timeout: 90 * time.Second},
	}
}

type Page struct {
	ID             string
	Version        int
	Title          string
	URL            string
	UpdatedAt      string
	Storage        string
	NormalizedText string
	ContentHash    string
	Images         []domain.Image
}

func ExtractPageReferences(text string) []string {
	seen := map[string]bool{}
	var result []string
	for _, match := range pageURLPattern.FindAllStringSubmatch(text, -1) {
		if !seen[match[1]] {
			seen[match[1]] = true
			result = append(result, match[1])
		}
	}
	return result
}

func PageIDFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if id := parsed.Query().Get("pageId"); id != "" {
		return id
	}
	match := pageIDPattern.FindStringSubmatch(parsed.Path)
	if match != nil {
		return match[1]
	}
	return ""
}

func (c *Client) FetchPage(ctx context.Context, pageID string) (Page, error) {
	var payload struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		SpaceID string `json:"spaceId"`
		Version struct {
			Number    int    `json:"number"`
			CreatedAt string `json:"createdAt"`
		} `json:"version"`
		Body struct {
			Storage struct {
				Value string `json:"value"`
			} `json:"storage"`
		} `json:"body"`
		Links struct {
			WebUI string `json:"webui"`
		} `json:"_links"`
	}
	if err := c.getJSON(ctx, "/wiki/api/v2/pages/"+url.PathEscape(pageID)+"?body-format=storage", &payload); err != nil {
		return Page{}, err
	}
	text, embedded := NormalizeStorage(payload.Body.Storage.Value)
	if text == "" {
		return Page{}, fmt.Errorf("confluence page %s has an empty body", pageID)
	}
	attachments, err := c.fetchAttachments(ctx, pageID)
	if err != nil {
		return Page{}, err
	}
	images := orderEmbeddedImages(embedded, attachments)
	digest := sha256.Sum256([]byte(text))
	pageURL := c.baseURL + payload.Links.WebUI
	if payload.Links.WebUI == "" {
		pageURL = c.baseURL + "/wiki/pages/viewpage.action?pageId=" + pageID
	}
	return Page{
		ID:             payload.ID,
		Version:        payload.Version.Number,
		Title:          payload.Title,
		URL:            pageURL,
		UpdatedAt:      payload.Version.CreatedAt,
		Storage:        payload.Body.Storage.Value,
		NormalizedText: text,
		ContentHash:    hex.EncodeToString(digest[:]),
		Images:         images,
	}, nil
}

type attachment struct {
	ID          string
	Version     int
	Title       string
	MediaType   string
	DownloadURL string
}

func (c *Client) fetchAttachments(ctx context.Context, pageID string) ([]attachment, error) {
	var result []attachment
	cursor := ""
	for {
		endpoint := "/wiki/api/v2/pages/" + url.PathEscape(pageID) + "/attachments?limit=100"
		if cursor != "" {
			endpoint += "&cursor=" + url.QueryEscape(cursor)
		}
		var payload struct {
			Results []struct {
				ID        string `json:"id"`
				Title     string `json:"title"`
				MediaType string `json:"mediaType"`
				Version   struct {
					Number int `json:"number"`
				} `json:"version"`
				Links struct {
					Download string `json:"download"`
				} `json:"_links"`
			} `json:"results"`
			Links struct {
				Next string `json:"next"`
			} `json:"_links"`
		}
		if err := c.getJSON(ctx, endpoint, &payload); err != nil {
			return nil, err
		}
		for _, item := range payload.Results {
			result = append(result, attachment{
				ID: item.ID, Version: item.Version.Number, Title: item.Title,
				MediaType: item.MediaType, DownloadURL: item.Links.Download,
			})
		}
		if payload.Links.Next == "" {
			break
		}
		next, err := url.Parse(payload.Links.Next)
		if err != nil {
			break
		}
		cursor = next.Query().Get("cursor")
		if cursor == "" {
			break
		}
	}
	return result, nil
}

func orderEmbeddedImages(embedded []string, attachments []attachment) []domain.Image {
	byName := map[string]attachment{}
	for _, item := range attachments {
		if strings.HasPrefix(item.MediaType, "image/") {
			byName[item.Title] = item
		}
	}
	seen := map[string]bool{}
	var result []domain.Image
	for _, filename := range embedded {
		item, ok := byName[filename]
		if !ok || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		result = append(result, domain.Image{
			AttachmentID: item.ID,
			Version:      item.Version,
			Filename:     item.Title,
			MediaType:    item.MediaType,
			DownloadURL:  item.DownloadURL,
			Order:        len(result),
		})
	}
	return result
}

func (c *Client) Download(ctx context.Context, downloadURL string) ([]byte, error) {
	endpoint := downloadURL
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = c.baseURL + "/" + strings.TrimLeft(endpoint, "/")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(c.email, c.token)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download confluence attachment: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download confluence attachment returned HTTP %d", response.StatusCode)
	}
	const maxImageBytes = 15 << 20
	content, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxImageBytes {
		return nil, fmt.Errorf("confluence image exceeds %d bytes", maxImageBytes)
	}
	return content, nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, result any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+endpoint, nil)
	if err != nil {
		return err
	}
	request.SetBasicAuth(c.email, c.token)
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("confluence request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("confluence API returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(raw)))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 20<<20)).Decode(result)
}

func NormalizeStorage(storage string) (string, []string) {
	document, err := html.Parse(bytes.NewBufferString(storage))
	if err != nil {
		return "", nil
	}
	var text strings.Builder
	var images []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			switch node.Data {
			case "p", "div", "br", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "table", "ul", "ol":
				text.WriteByte('\n')
			}
			if node.Data == "ri:attachment" {
				for _, attr := range node.Attr {
					if attr.Key == "ri:filename" {
						images = append(images, attr.Val)
					}
				}
			}
		}
		if node.Type == html.TextNode {
			value := strings.Join(strings.Fields(node.Data), " ")
			if value != "" {
				text.WriteString(value)
				text.WriteByte(' ')
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
		if node.Type == html.ElementNode {
			switch node.Data {
			case "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "table", "ul", "ol":
				text.WriteByte('\n')
			}
		}
	}
	walk(document)
	var lines []string
	for _, line := range strings.Split(text.String(), "\n") {
		if value := strings.TrimSpace(line); value != "" {
			lines = append(lines, value)
		}
	}
	return strings.Join(lines, "\n"), images
}

func ContentHash(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func SafeFilename(name string) string {
	value := path.Base(name)
	value = strings.ReplaceAll(value, "\x00", "")
	if value == "." || value == "/" || value == "" {
		return "confluence-image-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return value
}
