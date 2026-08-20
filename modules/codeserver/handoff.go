// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package codeserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
)

// HandoffPurpose identifies tokens issued to launch code-server.
const HandoffPurpose = "code-server-launch"

// HandoffLifetime matches the durable collaboration lifetime. The handoff is
// also the CodeServer-side Gitea credential, so a five-minute launch-only
// lifetime would silently break API and WebSocket access in an open workspace.
const HandoffLifetime = 12 * time.Hour

// Handoff contains the signed context exchanged between Gitea and code-server.
type Handoff struct {
	Purpose  string `json:"purpose"`
	Exp      int64  `json:"exp"`
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	RepoID   int64  `json:"repoId"`
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	CloneURL string `json:"cloneUrl"`
	Ref      string `json:"ref"`
	Path     string `json:"path,omitempty"`
	// CollaborationSessionID is set when a user joins a durable Gitea
	// collaboration session from another device.
	CollaborationSessionID string `json:"collaborationSession,omitempty"`
	BaseOwner              string `json:"baseOwner,omitempty"`
	BaseRepo               string `json:"baseRepo,omitempty"`
	BaseRepoID             int64  `json:"baseRepoId,omitempty"`
	BaseCloneURL           string `json:"baseCloneUrl,omitempty"`
	GiteaURL               string `json:"giteaUrl"`
}

func signPayload(payload, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// Sign creates a short-lived HMAC-signed handoff token.
func Sign(handoff Handoff, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("code-server shared secret is empty")
	}
	payload, err := json.Marshal(handoff)
	if err != nil {
		return "", err
	}
	encodedPayload := base64.RawURLEncoding.EncodeToString(payload)
	return encodedPayload + "." + signPayload(encodedPayload, secret), nil
}

// Verify checks a handoff token and returns its claims when valid.
func Verify(token, secret string, now time.Time) (*Handoff, bool) {
	if token == "" || secret == "" {
		return nil, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return nil, false
	}
	expected := signPayload(parts[0], secret)
	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	var handoff Handoff
	if err := json.Unmarshal(payload, &handoff); err != nil {
		return nil, false
	}
	if handoff.Purpose != HandoffPurpose || handoff.Exp <= now.Unix() || handoff.UserID <= 0 || handoff.Owner == "" || handoff.Repo == "" || handoff.CloneURL == "" || handoff.GiteaURL == "" {
		return nil, false
	}
	return &handoff, true
}

// LaunchURL appends a signed handoff token to the configured code-server URL.
func LaunchURL(baseURL string, handoff Handoff, secret string) (string, error) {
	token, err := Sign(handoff, secret)
	if err != nil {
		return "", err
	}
	launchURL, err := url.Parse(baseURL)
	if err != nil || launchURL.Scheme == "" || launchURL.Host == "" {
		return "", errors.New("code-server URL must be absolute")
	}
	query := launchURL.Query()
	query.Set("token", token)
	launchURL.RawQuery = query.Encode()
	return launchURL.String(), nil
}
