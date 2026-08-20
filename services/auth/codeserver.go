// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"

	user_model "gitea.dev/models/user"
	"gitea.dev/modules/codeserver"
	"gitea.dev/modules/setting"
)

var _ Method = &CodeServer{}

// CodeServer authenticates the short-lived handoff sent by code-server API calls.
type CodeServer struct{}

// CodeServerHandoffDataKey is the request-data key populated when the
// CodeServer authentication method validates a signed launch handoff.
// API handlers use it to bind a request to the repository that was launched;
// the user id alone is not enough for that check.
const CodeServerHandoffDataKey = "CodeServerHandoff"

// Name returns the authentication method name.
func (c *CodeServer) Name() string {
	return "code_server"
}

// Verify authenticates a CodeServer authorization header.
func (c *CodeServer) Verify(req *http.Request, _ http.ResponseWriter, store DataStore, _ SessionStore) (*user_model.User, error) {
	const prefix = "CodeServer "
	authorization := req.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, prefix) {
		return nil, nil //nolint:nilnil // the auth method is not applicable
	}
	if !setting.CodeServer.Enabled || setting.CodeServer.SharedSecret == "" {
		return nil, errors.New("code-server authentication is disabled")
	}

	handoff, ok := codeserver.Verify(strings.TrimSpace(strings.TrimPrefix(authorization, prefix)), setting.CodeServer.SharedSecret, time.Now())
	if !ok {
		return nil, errors.New("invalid code-server handoff token")
	}

	user, err := user_model.GetUserByID(req.Context(), handoff.UserID)
	if err != nil {
		return nil, err
	}
	store.GetData()[CodeServerHandoffDataKey] = handoff
	return user, nil
}
