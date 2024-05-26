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

	"github.com/mattn/go-mastodon"
)

func main() {
	outputDir := os.Getenv("OUTPUT_DIR")
	if outputDir == "" {
		outputDir = "statuses"
	}
	config := &mastodon.Config{
		Server:       os.Getenv("SERVER_ENDPOINT"),
		ClientID:     os.Getenv("OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
		AccessToken:  os.Getenv("OAUTH_ACCESS_TOKEN"),
	}
	client := mastodon.NewClient(config)
	ctx := context.Background()
	if err := fetchUpdates(ctx, client, outputDir); err != nil {
		log.Fatal(err)
	}
}

func fetchUpdates(ctx context.Context, client *mastodon.Client, outputDir string) error {
	slog.Info("starting to fetch updates", "output-dir", outputDir)
	defer slog.Info("finished fetching updates", "output-dir", outputDir)

	acct, err := client.GetAccountCurrentUser(context.Background())
	if err != nil {
		return err
	}

	limit := int64(40)
	var lastID mastodon.ID
	for {
		statuses, err := client.GetAccountStatuses(ctx, acct.ID, &mastodon.Pagination{
			MaxID: lastID,
			Limit: limit,
		})
		if err != nil {
			return err
		}
		slog.Info("processing more statuses", "count", len(statuses))
		for _, status := range statuses {
			year, month, day := status.CreatedAt.Date()
			basepath := filepath.Join(outputDir, fmt.Sprintf("%04d/%02d/%02d/%s", year, month, day, string(status.ID)))
			if err := saveStatus(status, basepath); err != nil {
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
					if err := saveStatus(descendant, filepath.Join(basepath, "replies", string(descendant.ID))); err != nil {
						return err
					}
				}
			}
		}
		if len(statuses) > 0 {
			lastID = statuses[len(statuses)-1].ID
		} else {
			break
		}
	}

	return nil
}

func saveStatus(status *mastodon.Status, prefix string) error {
	if err := os.MkdirAll(prefix, 0700); err != nil {
		return err
	}

	if status.Card != nil && status.Card.Image != "" {
		ext := filepath.Ext(status.Card.Image)
		outputPath := filepath.Join(prefix, "card_image"+ext)
		if err := saveMedia(status.Card.Image, outputPath); err != nil {
			slog.Error("downloading cover image:", "error", err)
		} else {
			status.Card.Image = filepath.Base(outputPath)
		}
	}
	for _, mediaAttachment := range status.MediaAttachments {
		if mediaAttachment.RemoteURL == "" {
			continue
		}
		ext := filepath.Ext(mediaAttachment.RemoteURL)
		outputPath := filepath.Join(prefix, "media_attachment_"+string(mediaAttachment.ID)+ext)
		if err := saveMedia(mediaAttachment.RemoteURL, outputPath); err != nil {
			slog.Error("downloading media:", "error", err)
		} else {
			mediaAttachment.URL = filepath.Base(outputPath)
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
