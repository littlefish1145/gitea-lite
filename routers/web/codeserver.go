// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package web

import (
	"net/http"
	"strings"
	"time"

	codeserver_model "gitea.dev/models/codeserver"
	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	"gitea.dev/modules/codeserver"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
	"gitea.dev/services/context"
)

// CodeServerCollaborationJoin authenticates the invited Gitea user in the
// browser, verifies repository permission, and mints a fresh CodeServer
// handoff.  The share URL therefore works on a second device without sharing
// the owner's CodeServer cookie or Gitea API token.
func CodeServerCollaborationJoin(ctx *context.Context) {
	if !setting.CodeServer.Enabled {
		ctx.HTTPError(http.StatusNotFound)
		return
	}
	sessionID := strings.TrimSpace(ctx.FormString("session"))
	session, exists, err := codeserver_model.GetSession(ctx, sessionID)
	if err != nil {
		ctx.ServerError("GetCodeServerSession", err)
		return
	}
	if !exists || session.ExpiresUnix <= timeutil.TimeStampNow() {
		ctx.HTTPError(http.StatusNotFound, "collaboration session not found or expired")
		return
	}

	repo, err := repo_model.GetRepositoryByID(ctx, session.RepoID)
	if err != nil {
		ctx.NotFound(err)
		return
	}
	permission, err := access_model.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.ServerError("GetCodeServerRepoPermission", err)
		return
	}
	if !permission.CanRead(unit.TypeCode) {
		ctx.HTTPError(http.StatusForbidden, "you do not have read permission for this repository")
		return
	}
	if ctx.Doer.ID != session.OwnerID {
		if _, invited, err := codeserver_model.GetInvite(ctx, session.ID, ctx.Doer.ID); err != nil {
			ctx.ServerError("GetCodeServerInvite", err)
			return
		} else if !invited {
			ctx.HTTPError(http.StatusForbidden, "you are not invited to this collaboration session")
			return
		}
	}

	cloneLink := repo.CloneLink(ctx, ctx.Doer)
	cloneURL := cloneLink.HTTPS
	if cloneURL == "" {
		cloneURL = cloneLink.SSH
	}
	if cloneURL == "" {
		ctx.HTTPError(http.StatusInternalServerError, "repository has no clone URL")
		return
	}
	handoff := codeserver.Handoff{
		Purpose:                codeserver.HandoffPurpose,
		Exp:                    time.Now().Add(codeserver.HandoffLifetime).Unix(),
		UserID:                 ctx.Doer.ID,
		Username:               ctx.Doer.Name,
		RepoID:                 repo.ID,
		Owner:                  repo.OwnerName,
		Repo:                   repo.Name,
		CloneURL:               cloneURL,
		Ref:                    session.Ref,
		GiteaURL:               strings.TrimRight(setting.AppURL, "/"),
		CollaborationSessionID: session.ID,
	}
	if repo.IsFork {
		if err := repo.GetBaseRepo(ctx); err == nil && repo.BaseRepo != nil {
			handoff.BaseOwner = repo.BaseRepo.OwnerName
			handoff.BaseRepo = repo.BaseRepo.Name
			handoff.BaseRepoID = repo.BaseRepo.ID
			baseCloneLink := repo.BaseRepo.CloneLink(ctx, ctx.Doer)
			handoff.BaseCloneURL = baseCloneLink.HTTPS
			if handoff.BaseCloneURL == "" {
				handoff.BaseCloneURL = baseCloneLink.SSH
			}
		}
	}
	launchURL, err := codeserver.LaunchURL(setting.CodeServer.URL, handoff, setting.CodeServer.SharedSecret)
	if err != nil {
		ctx.ServerError("CreateCodeServerJoinHandoff", err)
		return
	}
	ctx.Redirect(launchURL)
}
