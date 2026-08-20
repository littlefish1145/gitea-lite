// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package codeserver

import (
	"strings"
	"testing"
	"time"
)

func testHandoff() Handoff {
	return Handoff{
		Purpose:  HandoffPurpose,
		Exp:      200,
		UserID:   7,
		Username: "alice",
		RepoID:   11,
		Owner:    "alice",
		Repo:     "demo",
		CloneURL: "https://gitea.example/alice/demo.git",
		Ref:      "main",
		GiteaURL: "https://gitea.example",
	}
}

func TestSignAndVerify(t *testing.T) {
	handoff := testHandoff()
	token, err := Sign(handoff, "shared-secret")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Verify(token, "shared-secret", time.Unix(100, 0))
	if !ok || got.Username != handoff.Username || got.Repo != handoff.Repo {
		t.Fatalf("Verify() = %#v, %v", got, ok)
	}
	if _, ok := Verify(token, "wrong-secret", time.Unix(100, 0)); ok {
		t.Fatal("Verify() accepted a token signed with another secret")
	}
}

func TestLaunchURL(t *testing.T) {
	launchURL, err := LaunchURL("https://code.example/gitea/open?source=gitea", testHandoff(), "shared-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(launchURL, "https://code.example/gitea/open?") || !strings.Contains(launchURL, "source=gitea") || !strings.Contains(launchURL, "token=") {
		t.Fatalf("unexpected launch URL: %s", launchURL)
	}
}

func TestHandoffLifetimeCoversCollaborationSession(t *testing.T) {
	if HandoffLifetime < 12*time.Hour {
		t.Fatalf("HandoffLifetime = %s, must cover a 12-hour collaboration session", HandoffLifetime)
	}
}
