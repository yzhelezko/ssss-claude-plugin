package updater

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/creativeprojects/go-selfupdate"
)

const (
	// Repository slug for GitHub releases
	RepoOwner = "yzhelezko"
	RepoName  = "ssss-claude-plugin"
)

// Updater handles automatic updates from GitHub releases
type Updater struct {
	currentVersion string
	repoSlug       selfupdate.RepositorySlug
	enabled        bool
	checkInterval  time.Duration
	lastCheck      time.Time
}

// UpdateResult contains the result of an update check
type UpdateResult struct {
	UpdateAvailable bool
	CurrentVersion  string
	LatestVersion   string
	ReleaseNotes    string
	ReleaseURL      string
	Updated         bool
	Error           error
}

// NewUpdater creates a new updater instance
func NewUpdater(currentVersion string, enabled bool) *Updater {
	return &Updater{
		currentVersion: currentVersion,
		repoSlug:       selfupdate.NewRepositorySlug(RepoOwner, RepoName),
		enabled:        enabled,
		checkInterval:  24 * time.Hour, // Check once per day
	}
}

// CheckForUpdate checks if a new version is available
func (u *Updater) CheckForUpdate(ctx context.Context) (*UpdateResult, error) {
	if !u.enabled {
		return &UpdateResult{
			UpdateAvailable: false,
			CurrentVersion:  u.currentVersion,
		}, nil
	}

	// Skip if version is "dev" (development mode)
	if u.currentVersion == "dev" || u.currentVersion == "" {
		log.Printf("[updater] Skipping update check for development version")
		return &UpdateResult{
			UpdateAvailable: false,
			CurrentVersion:  u.currentVersion,
		}, nil
	}

	latest, found, err := selfupdate.DetectLatest(ctx, u.repoSlug)
	if err != nil {
		return nil, fmt.Errorf("failed to detect latest version: %w", err)
	}

	if !found {
		return nil, fmt.Errorf("no releases found for %s/%s", RepoOwner, RepoName)
	}

	u.lastCheck = time.Now()

	result := &UpdateResult{
		CurrentVersion: u.currentVersion,
		LatestVersion:  latest.Version(),
		ReleaseNotes:   latest.ReleaseNotes,
		ReleaseURL:     latest.URL,
	}

	// Check if update is needed
	if latest.LessOrEqual(u.currentVersion) {
		result.UpdateAvailable = false
		return result, nil
	}

	result.UpdateAvailable = true
	return result, nil
}

// Update downloads and applies the latest update
func (u *Updater) Update(ctx context.Context) (*UpdateResult, error) {
	if !u.enabled {
		return nil, fmt.Errorf("auto-update is disabled")
	}

	// Skip if version is "dev" (development mode)
	if u.currentVersion == "dev" || u.currentVersion == "" {
		return nil, fmt.Errorf("cannot update development version")
	}

	latest, found, err := selfupdate.DetectLatest(ctx, u.repoSlug)
	if err != nil {
		return nil, fmt.Errorf("failed to detect latest version: %w", err)
	}

	if !found {
		return nil, fmt.Errorf("no releases found")
	}

	if latest.LessOrEqual(u.currentVersion) {
		return &UpdateResult{
			UpdateAvailable: false,
			CurrentVersion:  u.currentVersion,
			LatestVersion:   latest.Version(),
		}, nil
	}

	// Get executable path
	exe, err := selfupdate.ExecutablePath()
	if err != nil {
		return nil, fmt.Errorf("could not locate executable path: %w", err)
	}

	log.Printf("[updater] Updating from %s to %s...", u.currentVersion, latest.Version())

	// Perform the update
	if err := selfupdate.UpdateTo(ctx, latest.AssetURL, latest.AssetName, exe); err != nil {
		return nil, fmt.Errorf("failed to update binary: %w", err)
	}

	return &UpdateResult{
		UpdateAvailable: true,
		CurrentVersion:  u.currentVersion,
		LatestVersion:   latest.Version(),
		ReleaseNotes:    latest.ReleaseNotes,
		ReleaseURL:      latest.URL,
		Updated:         true,
	}, nil
}

// BackgroundCheck runs update check in background and logs results
func (u *Updater) BackgroundCheck(ctx context.Context) {
	if !u.enabled {
		return
	}

	go func() {
		// Small delay to not slow down startup
		time.Sleep(5 * time.Second)

		result, err := u.CheckForUpdate(ctx)
		if err != nil {
			log.Printf("[updater] Update check failed: %v", err)
			return
		}

		if result.UpdateAvailable {
			log.Printf("[updater] New version available: %s (current: %s)",
				result.LatestVersion, result.CurrentVersion)
			log.Printf("[updater] Release URL: %s", result.ReleaseURL)

			// Print to stderr for visibility in MCP server
			fmt.Fprintf(os.Stderr, "\n╔══════════════════════════════════════════════════════════╗\n")
			fmt.Fprintf(os.Stderr, "║  UPDATE AVAILABLE: %s → %s\n", result.CurrentVersion, result.LatestVersion)
			fmt.Fprintf(os.Stderr, "║  Run install script to update or enable auto-update\n")
			fmt.Fprintf(os.Stderr, "╚══════════════════════════════════════════════════════════╝\n\n")
		} else {
			log.Printf("[updater] Running latest version: %s", result.CurrentVersion)
		}
	}()
}

// BackgroundAutoUpdate runs update check and auto-updates if available
// If exitAfterUpdate is provided and true, the process will exit after a successful update
// so the MCP client can restart it with the new binary
func (u *Updater) BackgroundAutoUpdate(ctx context.Context, exitAfterUpdate ...bool) {
	if !u.enabled {
		return
	}

	shouldExit := len(exitAfterUpdate) > 0 && exitAfterUpdate[0]

	go func() {
		// Small delay to not slow down startup
		time.Sleep(5 * time.Second)

		result, err := u.Update(ctx)
		if err != nil {
			log.Printf("[updater] Auto-update failed: %v", err)
			return
		}

		if result.Updated {
			fmt.Fprintf(os.Stderr, "\n╔══════════════════════════════════════════════════════════╗\n")
			fmt.Fprintf(os.Stderr, "║  UPDATED: %s → %s\n", result.CurrentVersion, result.LatestVersion)
			if shouldExit {
				fmt.Fprintf(os.Stderr, "║  Restarting to apply update...\n")
			} else {
				fmt.Fprintf(os.Stderr, "║  Please restart to use the new version\n")
			}
			fmt.Fprintf(os.Stderr, "╚══════════════════════════════════════════════════════════╝\n\n")

			if shouldExit {
				// Give time for the message to be displayed
				time.Sleep(1 * time.Second)
				// Exit gracefully - MCP client will restart the server
				os.Exit(0)
			}
		}
	}()
}

// GetPlatformAssetName returns the expected asset name for current platform
func GetPlatformAssetName() string {
	os := runtime.GOOS
	arch := runtime.GOARCH

	ext := ""
	if os == "windows" {
		ext = ".zip"
	} else {
		ext = ".tar.gz"
	}

	return fmt.Sprintf("ssss-%s-%s%s", os, arch, ext)
}

// IsEnabled returns whether auto-update is enabled
func (u *Updater) IsEnabled() bool {
	return u.enabled
}

// CurrentVersion returns the current version
func (u *Updater) CurrentVersion() string {
	return u.currentVersion
}
