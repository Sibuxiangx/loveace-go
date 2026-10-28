// Package manifest ports manifest v2 and announcement fetching.
//
// Source: desktop/lib/services/manifest_service.dart,
// ios/loveaceios/Service/AnnouncementService.swift and the Android
// RemoteManifestService. The manifest is a public static file, so it bypasses
// the school auth gate. Endpoints are constants copied from the reference
// clients — no new URL is invented.
package manifest

import (
	"context"
	"encoding/json"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/timeutil"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// Notice is a manifest announcement.
type Notice struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Content             string   `json:"content"`
	Level               string   `json:"level"`
	Platforms           []string `json:"platforms"`
	Surfaces            []string `json:"surfaces"`
	PublishedAt         string   `json:"published_at"`
	ExpiresAt           *string  `json:"expires_at"`
	RequireConfirmation bool     `json:"require_confirmation"`
}

// IsVisibleInApp applies the iOS platform/surface/expiry filter.
func (n Notice) IsVisibleInApp(platform string) bool {
	if n.ID == "" {
		return false
	}
	platformOK := false
	for _, p := range n.Platforms {
		if p == "all" || p == platform {
			platformOK = true
			break
		}
	}
	if !platformOK {
		return false
	}
	surfaceOK := false
	for _, s := range n.Surfaces {
		if s == "app" {
			surfaceOK = true
			break
		}
	}
	if !surfaceOK {
		return false
	}
	return timeutil.IsUnexpired(n.ExpiresAt)
}

// Checksums are artifact checksums.
type Checksums struct {
	SHA256 *string `json:"sha256"`
	MD5    *string `json:"md5"`
}

// Artifact is a release artifact.
type Artifact struct {
	Type      string    `json:"type"`
	URL       string    `json:"url"`
	Arch      *string   `json:"arch"`
	Size      *int64    `json:"size"`
	Checksums Checksums `json:"checksums"`
}

// Release is a manifest release entry.
type Release struct {
	ID          string     `json:"id"`
	Version     string     `json:"version"`
	Build       *int64     `json:"build"`
	Channel     string     `json:"channel"`
	PublishedAt string     `json:"published_at"`
	Summary     string     `json:"summary"`
	Changelog   []string   `json:"changelog"`
	Artifacts   []Artifact `json:"artifacts"`
}

// PlatformManifest holds releases for one platform.
type PlatformManifest struct {
	MinimumSupportedBuild *int64    `json:"minimum_supported_build"`
	Releases              []Release `json:"releases"`
}

// V2 is the manifest v2 document.
type V2 struct {
	SchemaVersion int64                       `json:"schema_version"`
	Revision      string                      `json:"revision"`
	GeneratedAt   string                      `json:"generated_at"`
	Announcements []Notice                    `json:"announcements"`
	Platforms     map[string]PlatformManifest `json:"platforms"`
}

// VisibleAnnouncements filters announcements for a platform.
func (m *V2) VisibleAnnouncements(platform string) []Notice {
	out := make([]Notice, 0, len(m.Announcements))
	for _, notice := range m.Announcements {
		if notice.IsVisibleInApp(platform) {
			out = append(out, notice)
		}
	}
	return out
}

// Service fetches the manifest (bypasses the school auth gate).
type Service struct {
	s *session.Session
}

// New binds the service to a session.
func New(s *session.Session) *Service { return &Service{s: s} }

// FetchV2 fetches and validates Manifest v2, trying each known URL in order.
func (sv *Service) FetchV2(caller context.Context) (*V2, error) {
	ctx, release := sv.s.OperationContext(caller)
	defer release()
	urls := []string{config.ManifestURLPrimary, config.ManifestURLFallback}
	var lastErr error
	for _, rawURL := range urls {
		request := transport.NewRequest(transport.Get, rawURL).
			Header("Accept", "application/json").
			WithTimeout(8_000).
			WithMaxBytes(2 * 1024 * 1024)
		request.Operation = "manifest.v2"
		response, err := sv.s.Transport().Execute(ctx, request)
		if err != nil {
			lastErr = err
			continue
		}
		if response.Status < 200 || response.Status >= 300 {
			lastErr = errs.NewHTTP(response.Status, "manifest unavailable")
			continue
		}
		var value V2
		if err := json.Unmarshal(response.Body, &value); err != nil {
			lastErr = errs.NewParse("manifest decode failed: " + err.Error())
			continue
		}
		if value.SchemaVersion != 2 {
			lastErr = errs.NewUnsupportedManifestVersion(value.SchemaVersion)
			continue
		}
		return &value, nil
	}
	if lastErr == nil {
		lastErr = errs.NewTemporarilyUnavailable("no manifest URL reachable")
	}
	return nil, lastErr
}

// Announcements returns announcements visible for a platform.
func (sv *Service) Announcements(caller context.Context, platform string) ([]Notice, error) {
	m, err := sv.FetchV2(caller)
	if err != nil {
		return nil, err
	}
	return m.VisibleAnnouncements(platform), nil
}
