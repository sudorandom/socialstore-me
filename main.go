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

func main() {
	outputDir := os.Getenv("STATUS_OUTPUT_DIR")
	if outputDir == "" {
		outputDir = "statuses"
	}
	mediaOutputDir := os.Getenv("MEDIA_OUTPUT_DIR")
	if mediaOutputDir == "" {
		mediaOutputDir = "media"
	}
	config := &mastodon.Config{
		Server:       os.Getenv("SERVER_ENDPOINT"),
		ClientID:     os.Getenv("OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
		AccessToken:  os.Getenv("OAUTH_ACCESS_TOKEN"),
	}
	client := mastodon.NewClient(config)
	ctx := context.Background()
	if err := fetchUpdates(ctx, client, outputDir, mediaOutputDir); err != nil {
		log.Fatal(err)
	}
}

func fetchUpdates(ctx context.Context, client *mastodon.Client, outputDir, mediaOutputDir string) error {
	slog.Info("starting to fetch updates", "output-dir", outputDir)
	defer slog.Info("finished fetching updates", "output-dir", outputDir)

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
			basepath := filepath.Join(outputDir, fmt.Sprintf("%04d/%02d/%02d/%s", year, month, day, string(status.ID)))
			mediaBasepath := filepath.Join(mediaOutputDir, fmt.Sprintf("%04d/%02d/%02d/%s", year, month, day, string(status.ID)))
			if err := saveStatus(status, basepath, mediaBasepath); err != nil {
				return err
			}

			if status.RepliesCount > 0 {
				context, err := client.GetStatusContext(ctx, status.ID)
				if err != nil {
					return err
				}

				for _, descendant := range context.Descendants {
					// Note that we only care about direct descendants
					if descendant.InReplyToID != string(status.ID) {
						continue
					}
					prefix := filepath.Join(basepath, "replies", string(descendant.ID))
					if err := saveStatus(descendant, prefix, mediaBasepath); err != nil {
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
	if err := os.WriteFile(filepath.Join(outputDir, "index.json"), indexBytes, 0644); err != nil {
		return err
	}

	return nil
}

func saveStatus(status *mastodon.Status, prefix, mediaPrefix string) error {
	if err := os.MkdirAll(prefix, 0700); err != nil {
		return err
	}
	if err := os.MkdirAll(mediaPrefix, 0700); err != nil {
		return err
	}

	if status.Card != nil && status.Card.Image != "" {
		ext := filepath.Ext(status.Card.Image)
		outputPath := filepath.Join(mediaPrefix, "card_image"+ext)
		if err := saveMedia(status.Card.Image, outputPath); err != nil {
			slog.Error("downloading cover image:", "error", err)
		} else {
			status.Card.Image = outputPath
		}
	}
	for i, mediaAttachment := range status.MediaAttachments {
		if mediaAttachment.RemoteURL == "" {
			continue
		}
		ext := filepath.Ext(mediaAttachment.RemoteURL)
		outputPath := filepath.Join(mediaPrefix, "media_attachment_"+string(mediaAttachment.ID)+ext)
		if err := saveMedia(mediaAttachment.RemoteURL, outputPath); err != nil {
			slog.Error("downloading media:", "error", err)
		} else {
			status.MediaAttachments[i].URL = outputPath
		}
	}

	b, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(prefix, "status.json"), b, 0644); err != nil {
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
