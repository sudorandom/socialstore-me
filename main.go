package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mattn/go-mastodon"
)

type IndexFile struct {
	Statuses map[mastodon.ID]StatusEntry `json:"statuses"`
}

type StatusEntry struct {
	ID        mastodon.ID `json:"id"`
	Path      string      `json:"path"`
	CreatedAt time.Time   `json:"created_at"`
}

type Config struct {
	StatusDir string
	MediaDir  string
}

func (c Config) StatusPath(path string) string {
	return filepath.Join(c.StatusDir, path)
}

func (c Config) MediaPath(path string) string {
	return filepath.Join(c.MediaDir, path)
}

func main() {
	statusDir := os.Getenv("STATUS_OUTPUT_DIR")
	if statusDir == "" {
		statusDir = "statuses"
	}
	mediaDir := os.Getenv("MEDIA_OUTPUT_DIR")
	if mediaDir == "" {
		mediaDir = "media"
	}
	cfg := Config{
		StatusDir: statusDir,
		MediaDir:  mediaDir,
	}
	config := &mastodon.Config{
		Server:       os.Getenv("SERVER_ENDPOINT"),
		ClientID:     os.Getenv("OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
		AccessToken:  os.Getenv("OAUTH_ACCESS_TOKEN"),
	}
	client := mastodon.NewClient(config)
	ctx := context.Background()
	if err := fetchUpdates(ctx, cfg, client); err != nil {
		log.Fatal(err)
	}
}

func fetchUpdates(ctx context.Context, cfg Config, client *mastodon.Client) error {
	slog.Info("starting to fetch updates", "status-dir", cfg.StatusDir, "media-dir", cfg.MediaDir)
	defer slog.Info("finished fetching updates", "status-dir", cfg.StatusDir, "media-dir", cfg.MediaDir)

	acct, err := client.GetAccountCurrentUser(context.Background())
	if err != nil {
		return err
	}

	statusIndex := map[mastodon.ID]StatusEntry{}

	limit := int64(40)
	var lastID mastodon.ID
	var runningTotal int
	for {
		statuses, err := client.GetAccountStatuses(ctx, acct.ID, &mastodon.Pagination{
			MaxID: lastID,
			Limit: limit,
		})
		if err != nil {
			return err
		}
		runningTotal += len(statuses)
		slog.Info("processing more statuses", "count", len(statuses), "total", runningTotal)
		for _, status := range statuses {
			year, month, day := status.CreatedAt.Date()
			basepath := filepath.Join(fmt.Sprintf("%04d/%02d/%02d/%s", year, month, day, string(status.ID)))
			if err := saveStatus(cfg, status, basepath); err != nil {
				return err
			}

			if status.RepliesCount > 0 {
				context, err := client.GetStatusContext(ctx, status.ID)
				if err != nil {
					return err
				}

				prefixes := map[string]string{}

				for _, descendant := range context.Descendants {
					// Note that we only care about direct descendants
					var prefix string
					if descendant.InReplyToID == string(status.ID) {
						prefix = filepath.Join(basepath, "replies", string(descendant.ID))
					} else {
						inReplyToID := descendant.InReplyToID.(string)
						parentPrefix, ok := prefixes[inReplyToID]
						if !ok {
							slog.Info("unable to parent reply", "in-reply-to", descendant.InReplyToID)
							continue
						}
						prefix = filepath.Join(parentPrefix, "replies", string(descendant.ID))
					}
					prefixes[string(descendant.ID)] = prefix
					if err := saveStatus(cfg, descendant, prefix); err != nil {
						return err
					}
				}
			}

			statusIndex[status.ID] = StatusEntry{
				ID:        status.ID,
				Path:      filepath.Join(basepath, "status.json"),
				CreatedAt: status.CreatedAt,
			}
		}
		if len(statuses) > 0 {
			lastID = statuses[len(statuses)-1].ID
		} else {
			break
		}
	}

	indexBytes, err := json.MarshalIndent(IndexFile{Statuses: statusIndex}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cfg.StatusPath("index.json")), indexBytes, 0644); err != nil {
		return err
	}

	return nil
}

func saveStatus(cfg Config, status *mastodon.Status, prefix string) error {
	if err := os.MkdirAll(cfg.StatusPath(prefix), 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.MediaPath(prefix), 0700); err != nil {
		return err
	}

	if status.Card != nil && status.Card.Image != "" {
		ext := filepath.Ext(status.Card.Image)
		relPath := filepath.Join(prefix, "card_image"+ext)
		if err := saveMedia(status.Card.Image, cfg.MediaPath(relPath)); err != nil {
			slog.Error("downloading cover image:", "error", err)
		} else {
			status.Card.Image = relPath
		}
	}
	for i, mediaAttachment := range status.MediaAttachments {
		url := mediaAttachment.URL
		if mediaAttachment.RemoteURL != "" {
			url = mediaAttachment.RemoteURL
		}
		if url == "" {
			continue
		}
		ext := filepath.Ext(url)
		relPath := filepath.Join(prefix, "media_attachment_"+string(mediaAttachment.ID)+ext)
		if err := saveMedia(url, cfg.MediaPath(relPath)); err != nil {
			slog.Error("downloading media:", "error", err)
		} else {
			status.MediaAttachments[i].RemoteURL = url
			status.MediaAttachments[i].URL = relPath
		}
	}

	b, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	relPath := filepath.Join(prefix, "status.json")
	if err := os.WriteFile(cfg.StatusPath(relPath), b, 0644); err != nil {
		return err
	}
	return nil
}

func saveMedia(inputURL string, outputPath string) error {
	_, err := os.Stat(outputPath)
	if err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	slog.Info("saveMedia", "inputURL", inputURL, "outputPath", outputPath)
	resp, err := http.Get(inputURL)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("unexpected status when downloading '%s': %s", inputURL, resp.Status)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if err := os.WriteFile(outputPath, body, 0644); err != nil {
		return err
	}
	return nil
}
