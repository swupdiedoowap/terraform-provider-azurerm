package notificationhubs

// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See NOTICE.txt in the project root for license information.

type AdmCredentialProperties struct {
	AuthTokenUrl *string `json:"authTokenUrl,omitempty"`
	ClientId     *string `json:"clientId,omitempty"`
	ClientSecret *string `json:"clientSecret,omitempty"`
}
