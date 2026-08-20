// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1

import (
	gocontext "context"
	"crypto/rand"
	"encoding/hex"
	stdjson "encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	codeserver_model "gitea.dev/models/codeserver"
	"gitea.dev/models/db"
	access_model "gitea.dev/models/perm/access"
	repo_model "gitea.dev/models/repo"
	"gitea.dev/models/unit"
	user_model "gitea.dev/models/user"
	"gitea.dev/modules/codeserver"
	"gitea.dev/modules/graceful"
	"gitea.dev/modules/json"
	"gitea.dev/modules/log"
	"gitea.dev/modules/setting"
	"gitea.dev/modules/timeutil"
	"gitea.dev/modules/web"
	auth_service "gitea.dev/services/auth"
	"gitea.dev/services/context"
	"gitea.dev/services/pubsub"

	gitea_ws "github.com/coder/websocket"
)

const (
	codeServerSessionLifetime = 12 * time.Hour
	codeServerMessageLimit    = 100
)

var codeServerCollaborationColors = []string{
	"#3794ff", "#d18616", "#b180d7", "#89d185",
	"#f14c4c", "#75beff", "#cca700", "#4ec9b0",
}

type collaborationCreateRequest struct {
	RepoID int64  `json:"repo_id"`
	Ref    string `json:"ref" binding:"MaxSize(255)"`
}

type collaborationInviteRequest struct {
	Username string `json:"username" binding:"Required;MaxSize(255)"`
}

type collaborationReference struct {
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	Ref       string `json:"ref"`
	Path      string `json:"path"`
	StartLine int    `json:"startLine,omitempty"`
	EndLine   int    `json:"endLine,omitempty"`
	Column    int    `json:"column,omitempty"`
}

type collaborationMessageRequest struct {
	Body      string                  `json:"body" binding:"Required;MaxSize(10000)"`
	Reference *collaborationReference `json:"reference"`
}

type collaborationCursorRequest struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Column      int    `json:"column"`
	StartLine   int    `json:"startLine"`
	StartColumn int    `json:"startColumn"`
	EndLine     int    `json:"endLine"`
	EndColumn   int    `json:"endColumn"`
}

type collaborationWebSocketMessage struct {
	Type        string                  `json:"type"`
	Body        string                  `json:"body"`
	Reference   *collaborationReference `json:"reference"`
	Path        string                  `json:"path"`
	Line        int                     `json:"line"`
	Column      int                     `json:"column"`
	StartLine   int                     `json:"startLine"`
	StartColumn int                     `json:"startColumn"`
	EndLine     int                     `json:"endLine"`
	EndColumn   int                     `json:"endColumn"`
}

func newCodeServerSessionID() (string, error) {
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func codeServerShareURL(sessionID string) string {
	return strings.TrimRight(setting.AppURL, "/") + "/codeserver/collaboration/join?session=" + url.QueryEscape(sessionID)
}

func codeServerHandoff(ctx *context.APIContext) *codeserver.Handoff {
	handoff, _ := ctx.Data[auth_service.CodeServerHandoffDataKey].(*codeserver.Handoff)
	return handoff
}

func codeServerSessionForActor(ctx *context.APIContext, sessionID string, ownerOnly bool) (*codeserver_model.Session, *repo_model.Repository, []*codeserver_model.Invite, bool) {
	if sessionID == "" || len(sessionID) > 48 {
		ctx.APIErrorNotFound()
		return nil, nil, nil, false
	}

	session, exists, err := codeserver_model.GetSession(ctx, sessionID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, nil, false
	}
	if !exists || session.ExpiresUnix <= timeutil.TimeStampNow() {
		ctx.APIErrorNotFound()
		return nil, nil, nil, false
	}

	repo, err := repo_model.GetRepositoryByID(ctx, session.RepoID)
	if err != nil {
		ctx.APIErrorNotFound()
		return nil, nil, nil, false
	}
	permission, err := access_model.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, nil, false
	}
	if !permission.CanRead(unit.TypeCode) {
		ctx.APIError(http.StatusForbidden, "you do not have code read permission for this repository")
		return nil, nil, nil, false
	}

	if ownerOnly && session.OwnerID != ctx.Doer.ID {
		ctx.APIError(http.StatusForbidden, "only the collaboration owner can perform this action")
		return nil, nil, nil, false
	}
	if session.OwnerID != ctx.Doer.ID {
		if _, invited, err := codeserver_model.GetInvite(ctx, session.ID, ctx.Doer.ID); err != nil {
			ctx.APIErrorInternal(err)
			return nil, nil, nil, false
		} else if !invited {
			ctx.APIError(http.StatusForbidden, "you are not invited to this collaboration session")
			return nil, nil, nil, false
		}
	}

	invites, err := codeserver_model.ListInvites(ctx, session.ID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return nil, nil, nil, false
	}
	return session, repo, invites, true
}

func collaborationUser(ctx *context.APIContext, invite *codeserver_model.Invite, connected bool) map[string]any {
	participant := map[string]any{
		"userId":    invite.UserID,
		"username":  invite.Username,
		"avatarUrl": "",
		"color":     invite.Color,
		"connected": connected,
	}
	if user, err := user_model.GetUserByID(ctx, invite.UserID); err == nil {
		participant["username"] = user.Name
		participant["avatarUrl"] = user.AvatarLink(ctx)
	}
	return participant
}

func collaborationMessage(message *codeserver_model.Message) map[string]any {
	result := map[string]any{
		"id":        strconv.FormatInt(message.ID, 10),
		"userId":    message.UserID,
		"username":  message.Username,
		"body":      message.Body,
		"createdAt": time.Unix(int64(message.CreatedUnix), 0).UTC().Format(time.RFC3339),
	}
	if message.ReferenceJSON != "" {
		var reference collaborationReference
		if json.Unmarshal([]byte(message.ReferenceJSON), &reference) == nil {
			result["reference"] = reference
		}
	}
	return result
}

func collaborationSnapshot(ctx *context.APIContext, session *codeserver_model.Session, invites []*codeserver_model.Invite) (map[string]any, error) {
	messages, err := codeserver_model.ListMessages(ctx, session.ID, 0, codeServerMessageLimit)
	if err != nil {
		return nil, err
	}
	participants := make([]map[string]any, 0, len(invites))
	for _, invite := range invites {
		participants = append(participants, collaborationUser(ctx, invite, false))
	}
	messageData := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		messageData = append(messageData, collaborationMessage(message))
	}
	role := "collaborator"
	if session.OwnerID == ctx.Doer.ID {
		role = "owner"
	}
	return map[string]any{
		"id":           session.ID,
		"ownerId":      session.OwnerID,
		"role":         role,
		"selfUserId":   ctx.Doer.ID,
		"owner":        session.Owner,
		"repoId":       session.RepoID,
		"repo":         session.Repo,
		"ref":          session.Ref,
		"private":      session.Private,
		"createdAt":    time.Unix(int64(session.CreatedUnix), 0).UTC().Format(time.RFC3339),
		"expiresAt":    time.Unix(int64(session.ExpiresUnix), 0).UTC().Format(time.RFC3339),
		"participants": participants,
		"messages":     messageData,
	}, nil
}

func createCodeServerCollaboration(ctx *context.APIContext) {
	form := web.GetForm[*collaborationCreateRequest](ctx)
	handoff := codeServerHandoff(ctx)
	repoID := form.RepoID
	if repoID == 0 && handoff != nil {
		repoID = handoff.RepoID
	}
	if repoID == 0 {
		ctx.APIError(http.StatusBadRequest, "repo_id is required")
		return
	}

	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		ctx.APIErrorNotFound()
		return
	}
	if handoff != nil && (handoff.RepoID != repo.ID || !strings.EqualFold(handoff.Owner, repo.OwnerName) || !strings.EqualFold(handoff.Repo, repo.Name)) {
		ctx.APIError(http.StatusForbidden, "the collaboration repository does not match the launched repository")
		return
	}
	if repo.OwnerID != ctx.Doer.ID {
		ctx.APIError(http.StatusForbidden, "only the repository owner can start a collaboration session")
		return
	}
	permission, err := access_model.GetDoerRepoPermission(ctx, repo, ctx.Doer)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if !permission.CanRead(unit.TypeCode) {
		ctx.APIError(http.StatusForbidden, "you do not have code read permission for this repository")
		return
	}

	sessionID, err := newCodeServerSessionID()
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ref := strings.TrimSpace(form.Ref)
	if ref == "" && handoff != nil {
		ref = handoff.Ref
	}
	if len(ref) > 255 {
		ctx.APIError(http.StatusBadRequest, "ref is too long")
		return
	}
	session := &codeserver_model.Session{
		ID:          sessionID,
		OwnerID:     ctx.Doer.ID,
		RepoID:      repo.ID,
		Owner:       repo.OwnerName,
		Repo:        repo.Name,
		Ref:         ref,
		Private:     repo.IsPrivate,
		CreatedUnix: timeutil.TimeStampNow(),
		ExpiresUnix: timeutil.TimeStampNow().AddDuration(codeServerSessionLifetime),
	}
	if err := dbInsertCodeServerSession(ctx, session); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if err := codeserver_model.AddInvite(ctx, &codeserver_model.Invite{
		SessionID:   session.ID,
		UserID:      ctx.Doer.ID,
		Username:    ctx.Doer.Name,
		Color:       codeServerCollaborationColors[0],
		CreatedUnix: timeutil.TimeStampNow(),
	}); err != nil {
		ctx.APIErrorInternal(err)
		return
	}

	invites, err := codeserver_model.ListInvites(ctx, session.ID)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	snapshot, err := collaborationSnapshot(ctx, session, invites)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusCreated, map[string]any{"session": snapshot, "shareUrl": codeServerShareURL(session.ID)})
}

// dbInsertCodeServerSession is kept as a small seam for the HTTP handlers and
// makes the write explicit instead of relying on the request's repository
// object to be persisted implicitly.
func dbInsertCodeServerSession(ctx *context.APIContext, session *codeserver_model.Session) error {
	return db.Insert(ctx, session)
}

func getCodeServerCollaboration(ctx *context.APIContext) {
	session, _, invites, ok := codeServerSessionForActor(ctx, ctx.PathParam("id"), false)
	if !ok {
		return
	}
	snapshot, err := collaborationSnapshot(ctx, session, invites)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{"session": snapshot, "shareUrl": codeServerShareURL(session.ID)})
}

func inviteCodeServerCollaboration(ctx *context.APIContext) {
	session, repo, invites, ok := codeServerSessionForActor(ctx, ctx.PathParam("id"), true)
	if !ok {
		return
	}
	form := web.GetForm[*collaborationInviteRequest](ctx)
	username := strings.TrimSpace(form.Username)
	invitee, err := user_model.GetUserByName(ctx, username)
	if err != nil {
		if user_model.IsErrUserNotExist(err) {
			ctx.APIError(http.StatusNotFound, "Gitea user not found")
		} else {
			ctx.APIErrorInternal(err)
		}
		return
	}
	if !invitee.IsActive || invitee.ProhibitLogin {
		ctx.APIError(http.StatusForbidden, "the invited Gitea user cannot sign in")
		return
	}
	permission, err := access_model.GetIndividualUserRepoPermission(ctx, repo, invitee)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	if !permission.CanRead(unit.TypeCode) {
		ctx.APIError(http.StatusForbidden, "the invited user does not have read permission for this repository")
		return
	}
	if err := codeserver_model.AddInvite(ctx, &codeserver_model.Invite{
		SessionID:   session.ID,
		UserID:      invitee.ID,
		Username:    invitee.Name,
		Color:       codeServerCollaborationColors[len(invites)%len(codeServerCollaborationColors)],
		CreatedUnix: timeutil.TimeStampNow(),
	}); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	ctx.JSON(http.StatusOK, map[string]any{
		"username":  invitee.Name,
		"avatarUrl": invitee.AvatarLink(ctx),
		"shareUrl":  codeServerShareURL(session.ID),
	})
}

func listCodeServerMessages(ctx *context.APIContext) {
	session, _, _, ok := codeServerSessionForActor(ctx, ctx.PathParam("id"), false)
	if !ok {
		return
	}
	afterID, err := strconv.ParseInt(ctx.FormString("after"), 10, 64)
	if err != nil {
		afterID = 0
	}
	messages, err := codeserver_model.ListMessages(ctx, session.ID, afterID, codeServerMessageLimit)
	if err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	result := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		result = append(result, collaborationMessage(message))
	}
	ctx.JSON(http.StatusOK, result)
}

func addCodeServerMessage(ctx *context.APIContext) {
	session, _, _, ok := codeServerSessionForActor(ctx, ctx.PathParam("id"), false)
	if !ok {
		return
	}
	form := web.GetForm[*collaborationMessageRequest](ctx)
	body := strings.TrimSpace(form.Body)
	if body == "" {
		ctx.APIError(http.StatusBadRequest, "message body is empty")
		return
	}
	referenceJSON := ""
	if form.Reference != nil {
		if form.Reference.Owner == "" || form.Reference.Repo == "" || form.Reference.Ref == "" || form.Reference.Path == "" {
			ctx.APIError(http.StatusBadRequest, "invalid code reference")
			return
		}
		encoded, err := json.Marshal(form.Reference)
		if err != nil {
			ctx.APIErrorInternal(err)
			return
		}
		referenceJSON = string(encoded)
	}
	message := &codeserver_model.Message{
		SessionID:     session.ID,
		UserID:        ctx.Doer.ID,
		Username:      ctx.Doer.Name,
		Body:          body,
		ReferenceJSON: referenceJSON,
		CreatedUnix:   timeutil.TimeStampNow(),
	}
	if err := db.Insert(ctx, message); err != nil {
		ctx.APIErrorInternal(err)
		return
	}
	publishCodeServerEvent(session.ID, map[string]any{"type": "message", "message": collaborationMessage(message)})
	ctx.JSON(http.StatusCreated, collaborationMessage(message))
}

func publishCodeServerEvent(sessionID string, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		log.Error("codeserver collaboration event marshal failed: %v", err)
		return
	}
	pubsub.DefaultBroker.Publish(pubsub.CodeServerTopic(sessionID), payload)
}

func collaborationWebSocketParticipant(ctx *context.APIContext, invite *codeserver_model.Invite, connected bool, cursor map[string]any) map[string]any {
	participant := collaborationUser(ctx, invite, connected)
	if cursor != nil {
		participant["cursor"] = cursor
	}
	return participant
}

func collaborationWebSocket(ctx *context.APIContext) {
	if ctx.Req.Header.Get("Upgrade") == "" {
		ctx.Resp.Header().Set("Connection", "Upgrade")
		ctx.Resp.Header().Set("Upgrade", "websocket")
		ctx.Resp.WriteHeader(http.StatusUpgradeRequired)
		return
	}
	session, _, invites, ok := codeServerSessionForActor(ctx, ctx.PathParam("id"), false)
	if !ok {
		return
	}
	var invite *codeserver_model.Invite
	for _, candidate := range invites {
		if candidate.UserID == ctx.Doer.ID {
			invite = candidate
			break
		}
	}
	if invite == nil {
		ctx.APIError(http.StatusForbidden, "you are not invited to this collaboration session")
		return
	}
	log.Info("[CodeServerCollab] websocket authorized for session %s user %s", session.ID, invite.Username)

	conn, err := gitea_ws.Accept(ctx.Resp, ctx.Req, nil)
	if err != nil {
		log.Error("codeserver collaboration websocket accept failed: %v", err)
		return
	}
	log.Info("[CodeServerCollab] websocket upgraded for session %s", session.ID)
	defer conn.CloseNow() //nolint:errcheck // best-effort close

	snapshot, err := collaborationSnapshot(ctx, session, invites)
	if err != nil {
		log.Error("[CodeServerCollab] snapshot failed for session %s: %v", session.ID, err)
		return
	}
	write := func(value any) bool {
		payload, err := json.Marshal(value)
		if err != nil {
			log.Error("[CodeServerCollab] event marshal failed for session %s: %v", session.ID, err)
			return false
		}
		writeCtx, cancel := gocontext.WithTimeout(ctx.Req.Context(), 10*time.Second)
		defer cancel()
		if err := conn.Write(writeCtx, gitea_ws.MessageText, payload); err != nil {
			log.Warn("[CodeServerCollab] event write failed for session %s: %v", session.ID, err)
			return false
		}
		return true
	}
	events, cancel := pubsub.DefaultBroker.Subscribe(pubsub.CodeServerTopic(session.ID))
	defer cancel()
	log.Info("[CodeServerCollab] sending session frame for session %s", session.ID)
	if !write(map[string]any{"type": "session", "session": snapshot, "self": collaborationWebSocketParticipant(ctx, invite, true, nil)}) {
		return
	}
	log.Info("[CodeServerCollab] session frame sent for session %s", session.ID)
	publishCodeServerEvent(session.ID, map[string]any{
		"type":        "presence",
		"participant": collaborationWebSocketParticipant(ctx, invite, true, nil),
	})
	publishCodeServerEvent(session.ID, map[string]any{"type": "presence-query"})
	shutdownCtx := graceful.GetManager().ShutdownContext()
	wsCtx := ctx.Req.Context()
	var currentCursor map[string]any
	incoming := make(chan []byte, 8)
	go func() {
		for {
			_, payload, err := conn.Read(wsCtx)
			if err != nil {
				close(incoming)
				return
			}
			select {
			case incoming <- payload:
			case <-wsCtx.Done():
				return
			}
		}
	}()

	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case <-wsCtx.Done():
			publishCodeServerEvent(session.ID, map[string]any{
				"type":        "presence",
				"participant": collaborationWebSocketParticipant(ctx, invite, false, nil),
			})
			return
		case <-shutdownCtx.Done():
			return
		case <-pingTicker.C:
			pingCtx, cancel := gocontext.WithTimeout(wsCtx, 10*time.Second)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return
			}
		case payload, open := <-events:
			if !open {
				return
			}
			var event struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(payload, &event) == nil && event.Type == "presence-query" {
				publishCodeServerEvent(session.ID, map[string]any{
					"type":        "presence",
					"participant": collaborationWebSocketParticipant(ctx, invite, true, currentCursor),
				})
				continue
			}
			if !write(stdjson.RawMessage(payload)) {
				return
			}
		case payload, open := <-incoming:
			if !open {
				publishCodeServerEvent(session.ID, map[string]any{
					"type":        "presence",
					"participant": collaborationWebSocketParticipant(ctx, invite, false, nil),
				})
				return
			}
			var message collaborationWebSocketMessage
			if err := json.Unmarshal(payload, &message); err != nil {
				continue
			}
			switch message.Type {
			case "cursor":
				if message.Path == "" || message.Line < 1 || message.Line > 10_000_000 {
					continue
				}
				cursor := map[string]any{
					"path": message.Path[:min(len(message.Path), 2048)],
					"line": message.Line,
				}
				if message.Column > 0 {
					cursor["column"] = min(message.Column, 10_000_000)
				}
				if message.StartLine > 0 {
					cursor["startLine"] = min(message.StartLine, 10_000_000)
				}
				if message.StartColumn > 0 {
					cursor["startColumn"] = min(message.StartColumn, 10_000_000)
				}
				if message.EndLine > 0 {
					cursor["endLine"] = min(message.EndLine, 10_000_000)
				}
				if message.EndColumn > 0 {
					cursor["endColumn"] = min(message.EndColumn, 10_000_000)
				}
				currentCursor = cursor
				publishCodeServerEvent(session.ID, map[string]any{
					"type":        "cursor",
					"participant": collaborationWebSocketParticipant(ctx, invite, true, cursor),
				})
			case "message":
				body := strings.TrimSpace(message.Body)
				if body == "" || len(body) > 10_000 {
					continue
				}
				referenceJSON := ""
				if message.Reference != nil {
					encoded, err := json.Marshal(message.Reference)
					if err != nil {
						continue
					}
					referenceJSON = string(encoded)
				}
				stored := &codeserver_model.Message{
					SessionID: session.ID, UserID: ctx.Doer.ID, Username: ctx.Doer.Name,
					Body: body, ReferenceJSON: referenceJSON, CreatedUnix: timeutil.TimeStampNow(),
				}
				if err := db.Insert(ctx, stored); err != nil {
					continue
				}
				publishCodeServerEvent(session.ID, map[string]any{"type": "message", "message": collaborationMessage(stored)})
			}
		}
	}
}

// RegisterCodeServerRoutes adds the durable collaboration API to the v1 API.
func RegisterCodeServerRoutes(m *web.Router) {
	m.Group("/codeserver", func() {
		m.Post("/collaborations", reqToken(), bind(collaborationCreateRequest{}), createCodeServerCollaboration)
		m.Get("/collaborations/{id}", reqToken(), getCodeServerCollaboration)
		m.Post("/collaborations/{id}/invites", reqToken(), bind(collaborationInviteRequest{}), inviteCodeServerCollaboration)
		m.Get("/collaborations/{id}/messages", reqToken(), listCodeServerMessages)
		m.Post("/collaborations/{id}/messages", reqToken(), bind(collaborationMessageRequest{}), addCodeServerMessage)
		m.Get("/collaborations/{id}/ws", reqToken(), collaborationWebSocket)
	})
}
