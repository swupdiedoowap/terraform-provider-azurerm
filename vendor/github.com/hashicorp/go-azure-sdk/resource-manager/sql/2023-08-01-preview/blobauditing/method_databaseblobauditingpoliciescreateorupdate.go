package blobauditing

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/go-azure-helpers/resourcemanager/commonids"
	"github.com/hashicorp/go-azure-sdk/sdk/client"
	"github.com/hashicorp/go-azure-sdk/sdk/odata"
)

// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See NOTICE.txt in the project root for license information.

type DatabaseBlobAuditingPoliciesCreateOrUpdateOperationResponse struct {
	HttpResponse *http.Response
	OData        *odata.OData
	Model        *DatabaseBlobAuditingPolicy
}

// DatabaseBlobAuditingPoliciesCreateOrUpdate ...
func (c BlobAuditingClient) DatabaseBlobAuditingPoliciesCreateOrUpdate(ctx context.Context, id commonids.SqlDatabaseId, input DatabaseBlobAuditingPolicy) (result DatabaseBlobAuditingPoliciesCreateOrUpdateOperationResponse, err error) {
	opts := client.RequestOptions{
		ContentType: "application/json; charset=utf-8",
		ExpectedStatusCodes: []int{
			http.StatusCreated,
			http.StatusOK,
		},
		HttpMethod: http.MethodPut,
		Path:       fmt.Sprintf("%s/auditingSettings/default", id.ID()),
	}

	req, err := c.Client.NewRequest(ctx, opts)
	if err != nil {
		return
	}

	if err = req.Marshal(input); err != nil {
		return
	}

	var resp *client.Response
	resp, err = req.Execute(ctx)
	if resp != nil {
		result.OData = resp.OData
		result.HttpResponse = resp.Response
	}
	if err != nil {
		return
	}

	var model DatabaseBlobAuditingPolicy
	result.Model = &model

	if err = resp.Unmarshal(result.Model); err != nil {
		return
	}

	return
}
