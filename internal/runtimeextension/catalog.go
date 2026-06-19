package runtimeextension

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	PiPackageCatalogURL     = "https://pi.dev/packages?types=extension"
	ClaudePluginRegistryURL = "https://github.com/anthropics/claude-plugins-official"
)

type CatalogItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Provider    string `json:"provider"`
	Registry    string `json:"registry"`
	RegistryURL string `json:"registry_url"`
	InstallRef  string `json:"install_ref,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
}

type CatalogSourceError struct {
	Provider    string `json:"provider"`
	RegistryURL string `json:"registry_url"`
	Error       string `json:"error"`
}

func FetchDefaultCatalog(ctx context.Context) ([]CatalogItem, []CatalogSourceError) {
	client := &http.Client{Timeout: 8 * time.Second}
	var items []CatalogItem
	var errs []CatalogSourceError

	piItems, err := FetchPiPackageCatalog(ctx, client, PiPackageCatalogURL)
	if err != nil {
		errs = append(errs, CatalogSourceError{Provider: "pi", RegistryURL: PiPackageCatalogURL, Error: err.Error()})
	} else {
		items = append(items, piItems...)
	}

	claudeItems, err := FetchClaudePluginCatalog(ctx, client)
	if err != nil {
		errs = append(errs, CatalogSourceError{Provider: "claude_code", RegistryURL: ClaudePluginRegistryURL, Error: err.Error()})
	} else {
		items = append(items, claudeItems...)
	}

	return items, errs
}

func FetchPiPackageCatalog(ctx context.Context, client *http.Client, catalogURL string) ([]CatalogItem, error) {
	body, err := fetchText(ctx, client, catalogURL)
	if err != nil {
		return nil, err
	}
	return ParsePiPackageCatalog(body), nil
}

func ParsePiPackageCatalog(body string) []CatalogItem {
	articlePattern := regexp.MustCompile(`(?s)<article\b[^>]*data-package-card="true"[^>]*>.*?</article>`)
	descPattern := regexp.MustCompile(`(?s)<p class="packages-desc">(.*?)</p>`)
	var items []CatalogItem
	seen := map[string]bool{}
	for _, article := range articlePattern.FindAllString(body, -1) {
		name := html.UnescapeString(readHTMLAttr(article, "data-package-name"))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		description := ""
		if match := descPattern.FindStringSubmatch(article); len(match) == 2 {
			description = strings.TrimSpace(stripHTML(html.UnescapeString(match[1])))
		}
		items = append(items, CatalogItem{
			ID:          name,
			Name:        name,
			Description: description,
			Provider:    "pi",
			Registry:    "pi.dev/packages",
			RegistryURL: PiPackageCatalogURL,
			InstallRef:  "npm:" + name,
			SourceURL:   "https://pi.dev/packages/" + strings.TrimPrefix(name, "/"),
		})
	}
	return items
}

func FetchClaudePluginCatalog(ctx context.Context, client *http.Client) ([]CatalogItem, error) {
	var items []CatalogItem
	for _, dir := range []string{"plugins", "external_plugins"} {
		loaded, err := fetchClaudePluginDir(ctx, client, dir)
		if err != nil {
			return nil, err
		}
		items = append(items, loaded...)
	}
	return items, nil
}

func fetchClaudePluginDir(ctx context.Context, client *http.Client, dir string) ([]CatalogItem, error) {
	endpoint := "https://api.github.com/repos/anthropics/claude-plugins-official/contents/" + dir + "?ref=main"
	body, err := fetchText(ctx, client, endpoint)
	if err != nil {
		return nil, err
	}
	var entries []struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		HTMLURL string `json:"html_url"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal([]byte(body), &entries); err != nil {
		return nil, fmt.Errorf("decode Claude plugin catalog: %w", err)
	}
	items := make([]CatalogItem, 0, len(entries))
	for _, entry := range entries {
		if entry.Type != "dir" || strings.TrimSpace(entry.Name) == "" {
			continue
		}
		installRef := entry.Name + "@claude-plugins-official"
		items = append(items, CatalogItem{
			ID:          entry.Name,
			Name:        entry.Name,
			Description: "Claude Code plugin from anthropics/claude-plugins-official.",
			Provider:    "claude_code",
			Registry:    "anthropics/claude-plugins-official",
			RegistryURL: ClaudePluginRegistryURL,
			InstallRef:  installRef,
			SourceURL:   entry.HTMLURL,
		})
	}
	return items, nil
}

func fetchText(ctx context.Context, client *http.Client, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json,text/html;q=0.9,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func readHTMLAttr(tag string, name string) string {
	pattern := regexp.MustCompile(regexp.QuoteMeta(name) + `="([^"]*)"`)
	match := pattern.FindStringSubmatch(tag)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func stripHTML(value string) string {
	pattern := regexp.MustCompile(`<[^>]+>`)
	return pattern.ReplaceAllString(value, "")
}
