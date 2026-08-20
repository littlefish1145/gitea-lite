// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package codeserver

import "testing"

func TestCollaborationTableNamesAreNamespaced(t *testing.T) {
	if got := (Session{}).TableName(); got != "code_server_session" {
		t.Fatalf("unexpected session table name: %s", got)
	}
	if got := (Invite{}).TableName(); got != "code_server_invite" {
		t.Fatalf("unexpected invite table name: %s", got)
	}
	if got := (Message{}).TableName(); got != "code_server_message" {
		t.Fatalf("unexpected message table name: %s", got)
	}
}
