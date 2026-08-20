// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import "strings"

// CodeServerSettings configures the optional Gitea to code-server handoff.
type CodeServerSettings struct {
	Enabled      bool
	URL          string
	SharedSecret string
}

// CodeServer contains the configured code-server integration.
var CodeServer CodeServerSettings

func loadCodeServerFrom(rootCfg ConfigProvider) {
	section, err := rootCfg.GetSection("code-server")
	if err != nil || section == nil {
		CodeServer = CodeServerSettings{}
		return
	}

	CodeServer = CodeServerSettings{
		Enabled:      section.Key("ENABLED").MustBool(false),
		URL:          strings.TrimRight(section.Key("URL").MustString(""), "/"),
		SharedSecret: section.Key("SHARED_SECRET").MustString(""),
	}
	if CodeServer.Enabled && (CodeServer.URL == "" || CodeServer.SharedSecret == "") {
		CodeServer = CodeServerSettings{}
	}
}
