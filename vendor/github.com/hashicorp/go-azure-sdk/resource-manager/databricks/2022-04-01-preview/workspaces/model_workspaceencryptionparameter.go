package workspaces

// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See NOTICE.txt in the project root for license information.

type WorkspaceEncryptionParameter struct {
	Type  *CustomParameterType `json:"type,omitempty"`
	Value *Encryption          `json:"value,omitempty"`
}
