// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

// Package codeserver contains the persistent state used by the CodeServer
// collaboration bridge.  The state lives in Gitea so a share link remains
// valid when CodeServer is restarted or when another CodeServer instance
// handles the next request.
package codeserver

import (
	"context"

	"gitea.dev/models/db"
	"gitea.dev/modules/timeutil"

	"xorm.io/builder"
)

// Session is a durable collaboration session bound to one Gitea repository.
type Session struct {
	ID          string             `xorm:"pk CHAR(48)"`
	OwnerID     int64              `xorm:"INDEX NOT NULL"`
	RepoID      int64              `xorm:"INDEX NOT NULL"`
	Owner       string             `xorm:"VARCHAR(255) NOT NULL"`
	Repo        string             `xorm:"VARCHAR(255) NOT NULL"`
	Ref         string             `xorm:"VARCHAR(255) NOT NULL"`
	Private     bool               `xorm:"NOT NULL DEFAULT false"`
	CreatedUnix timeutil.TimeStamp `xorm:"INDEX created"`
	ExpiresUnix timeutil.TimeStamp `xorm:"INDEX"`
}

// TableName keeps the collaboration session separate from Gitea's auth.Session
// table, which also has the Go type name Session.
func (Session) TableName() string {
	return "code_server_session"
}

// Invite grants a Gitea user access to a collaboration session.  The user id
// is the authority; the username is retained only for display and audit.
type Invite struct {
	ID          int64              `xorm:"pk autoincr"`
	SessionID   string             `xorm:"UNIQUE(session_user) INDEX NOT NULL CHAR(48)"`
	UserID      int64              `xorm:"UNIQUE(session_user) INDEX NOT NULL"`
	Username    string             `xorm:"VARCHAR(255) NOT NULL"`
	Color       string             `xorm:"VARCHAR(32) NOT NULL"`
	CreatedUnix timeutil.TimeStamp `xorm:"INDEX created"`
}

func (Invite) TableName() string {
	return "code_server_invite"
}

// Message is a durable chat message.  ReferenceJSON stores an optional
// structured file/line reference so clients can render it as a jump link.
type Message struct {
	ID            int64              `xorm:"pk autoincr"`
	SessionID     string             `xorm:"INDEX NOT NULL CHAR(48)"`
	UserID        int64              `xorm:"INDEX NOT NULL"`
	Username      string             `xorm:"VARCHAR(255) NOT NULL"`
	Body          string             `xorm:"TEXT NOT NULL"`
	ReferenceJSON string             `xorm:"TEXT"`
	CreatedUnix   timeutil.TimeStamp `xorm:"INDEX created"`
}

func (Message) TableName() string {
	return "code_server_message"
}

func init() {
	db.RegisterModel(new(Session))
	db.RegisterModel(new(Invite))
	db.RegisterModel(new(Message))
}

// GetSession returns a session by its opaque share id.
func GetSession(ctx context.Context, id string) (*Session, bool, error) {
	return db.Get[Session](ctx, builder.Eq{"id": id})
}

// GetInvite returns the invite for a user, if one exists.
func GetInvite(ctx context.Context, sessionID string, userID int64) (*Invite, bool, error) {
	return db.Get[Invite](ctx, builder.Eq{"session_id": sessionID, "user_id": userID})
}

// ListInvites returns all invited users in stable insertion order.
func ListInvites(ctx context.Context, sessionID string) ([]*Invite, error) {
	var invites []*Invite
	if err := db.GetEngine(ctx).Where(builder.Eq{"session_id": sessionID}).Asc("id").Find(&invites); err != nil {
		return nil, err
	}
	return invites, nil
}

// AddInvite inserts or refreshes an invite without creating duplicates.
func AddInvite(ctx context.Context, invite *Invite) error {
	existing, ok, err := GetInvite(ctx, invite.SessionID, invite.UserID)
	if err != nil {
		return err
	}
	if ok {
		_, err = db.GetEngine(ctx).ID(existing.ID).Cols("username", "color").Update(invite)
		return err
	}
	return db.Insert(ctx, invite)
}

// ListMessages returns messages after afterID, capped to limit rows.
func ListMessages(ctx context.Context, sessionID string, afterID, limit int64) ([]*Message, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	query := db.GetEngine(ctx).Where(builder.Eq{"session_id": sessionID}).And("id > ?", afterID).Asc("id").Limit(int(limit))
	var messages []*Message
	if err := query.Find(&messages); err != nil {
		return nil, err
	}
	return messages, nil
}
