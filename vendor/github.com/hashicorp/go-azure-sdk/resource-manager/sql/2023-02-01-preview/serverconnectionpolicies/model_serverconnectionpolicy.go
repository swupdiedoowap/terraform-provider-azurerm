package serverconnectionpolicies

// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See NOTICE.txt in the project root for license information.

type ServerConnectionPolicy struct {
	Id         *string                           `json:"id,omitempty"`
	Kind       *string                           `json:"kind,omitempty"`
	Location   *string                           `json:"location,omitempty"`
	Name       *string                           `json:"name,omitempty"`
	Properties *ServerConnectionPolicyProperties `json:"properties,omitempty"`
	Type       *string                           `json:"type,omitempty"`
}
