// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package compute

import (
	"context"
	"fmt"

	"github.com/hashicorp/go-azure-helpers/lang/pointer"
	"github.com/hashicorp/go-azure-helpers/lang/response"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/identity"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2022-03-02/disks"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2022-03-03/galleryapplicationversions"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2023-03-01/virtualmachines"
	azValidate "github.com/hashicorp/terraform-provider-azurerm/helpers/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/services/compute/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/suppress"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/validation"
	"github.com/hashicorp/terraform-provider-azurerm/utils"
	"github.com/tombuildsstuff/kermit/sdk/compute/2023-03-01/compute"
)

func virtualMachineAdditionalCapabilitiesSchema() *pluginsdk.Schema {
	return &pluginsdk.Schema{
		Type:     pluginsdk.TypeList,
		Optional: true,
		MaxItems: 1,
		Elem: &pluginsdk.Resource{
			Schema: map[string]*pluginsdk.Schema{
				// TODO: confirm this command

				// NOTE: requires registration to use:
				// $ az feature show --namespace Microsoft.Compute --name UltraSSDWithVMSS
				// $ az provider register -n Microsoft.Compute
				"ultra_ssd_enabled": {
					Type:     pluginsdk.TypeBool,
					Optional: true,
					Default:  false,
				},
			},
		},
	}
}

func expandVirtualMachineAdditionalCapabilities(input []interface{}) *virtualmachines.AdditionalCapabilities {
	capabilities := virtualmachines.AdditionalCapabilities{}

	if len(input) > 0 {
		raw := input[0].(map[string]interface{})

		capabilities.UltraSSDEnabled = utils.Bool(raw["ultra_ssd_enabled"].(bool))
	}

	return &capabilities
}

func flattenVirtualMachineAdditionalCapabilities(input *virtualmachines.AdditionalCapabilities) []interface{} {
	if input == nil {
		return []interface{}{}
	}

	ultraSsdEnabled := false

	if input.UltraSSDEnabled != nil {
		ultraSsdEnabled = *input.UltraSSDEnabled
	}

	return []interface{}{
		map[string]interface{}{
			"ultra_ssd_enabled": ultraSsdEnabled,
		},
	}
}

func expandVirtualMachineIdentity(input []interface{}) (*compute.VirtualMachineIdentity, error) {
	expanded, err := identity.ExpandSystemAndUserAssignedMap(input)
	if err != nil {
		return nil, err
	}

	out := compute.VirtualMachineIdentity{
		Type: compute.ResourceIdentityType(string(expanded.Type)),
	}
	if expanded.Type == identity.TypeUserAssigned || expanded.Type == identity.TypeSystemAssignedUserAssigned {
		out.UserAssignedIdentities = make(map[string]*compute.UserAssignedIdentitiesValue)
		for k := range expanded.IdentityIds {
			out.UserAssignedIdentities[k] = &compute.UserAssignedIdentitiesValue{
				// intentionally empty
			}
		}
	}
	return &out, nil
}

func flattenVirtualMachineIdentity(input *compute.VirtualMachineIdentity) (*[]interface{}, error) {
	var transform *identity.SystemAndUserAssignedMap

	if input != nil {
		transform = &identity.SystemAndUserAssignedMap{
			Type:        identity.Type(string(input.Type)),
			IdentityIds: make(map[string]identity.UserAssignedIdentityDetails),
		}

		if input.PrincipalID != nil {
			transform.PrincipalId = *input.PrincipalID
		}
		if input.TenantID != nil {
			transform.TenantId = *input.TenantID
		}
		for k, v := range input.UserAssignedIdentities {
			transform.IdentityIds[k] = identity.UserAssignedIdentityDetails{
				ClientId:    v.ClientID,
				PrincipalId: v.PrincipalID,
			}
		}
	}
	return identity.FlattenSystemAndUserAssignedMap(transform)
}

func expandVirtualMachineNetworkInterfaceIDs(input []interface{}) *[]virtualmachines.NetworkInterfaceReference {
	output := make([]virtualmachines.NetworkInterfaceReference, 0)

	for i, v := range input {
		output = append(output, virtualmachines.NetworkInterfaceReference{
			Id: utils.String(v.(string)),
			Properties: &virtualmachines.NetworkInterfaceReferenceProperties{
				Primary: utils.Bool(i == 0),
			},
		})
	}

	return &output
}

func flattenVirtualMachineNetworkInterfaceIDs(input *[]virtualmachines.NetworkInterfaceReference) []interface{} {
	if input == nil {
		return []interface{}{}
	}

	output := make([]interface{}, 0)

	for _, v := range *input {
		if v.Id == nil {
			continue
		}

		output = append(output, *v.Id)
	}

	return output
}

func virtualMachineOSDiskSchema() *pluginsdk.Schema {
	return &pluginsdk.Schema{
		Type:     pluginsdk.TypeList,
		Required: true,
		MaxItems: 1,
		Elem: &pluginsdk.Resource{
			Schema: map[string]*pluginsdk.Schema{
				"caching": {
					Type:     pluginsdk.TypeString,
					Required: true,
					ValidateFunc: validation.StringInSlice([]string{
						string(compute.CachingTypesNone),
						string(compute.CachingTypesReadOnly),
						string(compute.CachingTypesReadWrite),
					}, false),
				},
				"storage_account_type": {
					Type:     pluginsdk.TypeString,
					Required: true,
					// whilst this appears in the Update block the API returns this when changing:
					// Changing property 'osDisk.managedDisk.storageAccountType' is not allowed
					ForceNew: true,
					ValidateFunc: validation.StringInSlice([]string{
						// note: OS Disks don't support Ultra SSDs or PremiumV2_LRS
						string(compute.StorageAccountTypesPremiumLRS),
						string(compute.StorageAccountTypesStandardLRS),
						string(compute.StorageAccountTypesStandardSSDLRS),
						string(compute.StorageAccountTypesStandardSSDZRS),
						string(compute.StorageAccountTypesPremiumZRS),
					}, false),
				},

				// Optional
				"diff_disk_settings": {
					Type:     pluginsdk.TypeList,
					Optional: true,
					ForceNew: true,
					MaxItems: 1,
					Elem: &pluginsdk.Resource{
						Schema: map[string]*pluginsdk.Schema{
							"option": {
								Type:     pluginsdk.TypeString,
								Required: true,
								ForceNew: true,
								ValidateFunc: validation.StringInSlice([]string{
									string(compute.DiffDiskOptionsLocal),
								}, false),
							},
							"placement": {
								Type:     pluginsdk.TypeString,
								Optional: true,
								ForceNew: true,
								Default:  string(compute.DiffDiskPlacementCacheDisk),
								ValidateFunc: validation.StringInSlice([]string{
									string(compute.DiffDiskPlacementCacheDisk),
									string(compute.DiffDiskPlacementResourceDisk),
								}, false),
							},
						},
					},
				},

				"disk_encryption_set_id": {
					Type:     pluginsdk.TypeString,
					Optional: true,
					// the Compute/VM API is broken and returns the Resource Group name in UPPERCASE
					DiffSuppressFunc: suppress.CaseDifference,
					ValidateFunc:     validate.DiskEncryptionSetID,
					ConflictsWith:    []string{"os_disk.0.secure_vm_disk_encryption_set_id"},
				},

				"disk_size_gb": {
					Type:         pluginsdk.TypeInt,
					Optional:     true,
					Computed:     true,
					ValidateFunc: validation.IntBetween(0, 4095),
				},

				"name": {
					Type:     pluginsdk.TypeString,
					Optional: true,
					ForceNew: true,
					Computed: true,
				},

				"secure_vm_disk_encryption_set_id": {
					Type:          pluginsdk.TypeString,
					Optional:      true,
					ForceNew:      true,
					ValidateFunc:  validate.DiskEncryptionSetID,
					ConflictsWith: []string{"os_disk.0.disk_encryption_set_id"},
				},

				"security_encryption_type": {
					Type:     pluginsdk.TypeString,
					Optional: true,
					ForceNew: true,
					ValidateFunc: validation.StringInSlice([]string{
						string(compute.SecurityEncryptionTypesVMGuestStateOnly),
						string(compute.SecurityEncryptionTypesDiskWithVMGuestState),
					}, false),
				},

				"write_accelerator_enabled": {
					Type:     pluginsdk.TypeBool,
					Optional: true,
					Default:  false,
				},
			},
		},
	}
}

func expandVirtualMachineOSDisk(input []interface{}, osType virtualmachines.OperatingSystemTypes) (*virtualmachines.OSDisk, error) {
	raw := input[0].(map[string]interface{})
	caching := raw["caching"].(string)
	disk := virtualmachines.OSDisk{
		Caching: pointer.To(virtualmachines.CachingTypes(caching)),
		ManagedDisk: &virtualmachines.ManagedDiskParameters{
			StorageAccountType: pointer.To(virtualmachines.StorageAccountTypes(raw["storage_account_type"].(string))),
		},
		WriteAcceleratorEnabled: utils.Bool(raw["write_accelerator_enabled"].(bool)),

		// these have to be hard-coded so there's no point exposing them
		// for CreateOption, whilst it's possible for this to be "Attach" for an OS Disk
		// from what we can tell this approach has been superseded by provisioning from
		// an image of the machine (e.g. an Image/Shared Image Gallery)
		CreateOption: virtualmachines.DiskCreateOptionTypesFromImage,
		OsType:       pointer.To(osType),
	}

	securityEncryptionType := raw["security_encryption_type"].(string)
	if securityEncryptionType != "" {
		disk.ManagedDisk.SecurityProfile = &virtualmachines.VMDiskSecurityProfile{
			SecurityEncryptionType: pointer.To(virtualmachines.SecurityEncryptionTypes(securityEncryptionType)),
		}
	}
	if secureVMDiskEncryptionId := raw["secure_vm_disk_encryption_set_id"].(string); secureVMDiskEncryptionId != "" {
		if compute.SecurityEncryptionTypesDiskWithVMGuestState != compute.SecurityEncryptionTypes(securityEncryptionType) {
			return nil, fmt.Errorf("`secure_vm_disk_encryption_set_id` can only be specified when `security_encryption_type` is set to `DiskWithVMGuestState`")
		}
		disk.ManagedDisk.SecurityProfile.DiskEncryptionSet = &virtualmachines.SubResource{
			Id: utils.String(secureVMDiskEncryptionId),
		}
	}

	if osDiskSize := raw["disk_size_gb"].(int); osDiskSize > 0 {
		disk.DiskSizeGB = utils.Int64(int64(osDiskSize))
	}

	if diffDiskSettingsRaw := raw["diff_disk_settings"].([]interface{}); len(diffDiskSettingsRaw) > 0 {
		if caching != string(virtualmachines.CachingTypesReadOnly) {
			// Restriction per https://docs.microsoft.com/azure/virtual-machines/ephemeral-os-disks-deploy#vm-template-deployment
			return nil, fmt.Errorf("`diff_disk_settings` can only be set when `caching` is set to `ReadOnly`")
		}

		diffDiskRaw := diffDiskSettingsRaw[0].(map[string]interface{})
		disk.DiffDiskSettings = &virtualmachines.DiffDiskSettings{
			Option:    pointer.To(virtualmachines.DiffDiskOptions(diffDiskRaw["option"].(string))),
			Placement: pointer.To(virtualmachines.DiffDiskPlacement(diffDiskRaw["placement"].(string))),
		}
	}

	if id := raw["disk_encryption_set_id"].(string); id != "" {
		disk.ManagedDisk.DiskEncryptionSet = &virtualmachines.SubResource{
			Id: utils.String(id),
		}
	}

	if name := raw["name"].(string); name != "" {
		disk.Name = utils.String(name)
	}

	return &disk, nil
}

func flattenVirtualMachineOSDisk(ctx context.Context, disksClient *disks.DisksClient, input *virtualmachines.OSDisk) ([]interface{}, error) {
	if input == nil {
		return []interface{}{}, nil
	}

	diffDiskSettings := make([]interface{}, 0)
	if input.DiffDiskSettings != nil {
		placement := string(virtualmachines.DiffDiskPlacementCacheDisk)
		if pointer.From(input.DiffDiskSettings.Placement) != "" {
			placement = string(pointer.From(input.DiffDiskSettings.Placement))
		}

		diffDiskSettings = append(diffDiskSettings, map[string]interface{}{
			"option":    string(pointer.From(input.DiffDiskSettings.Option)),
			"placement": placement,
		})
	}

	diskSizeGb := 0
	if input.DiskSizeGB != nil && *input.DiskSizeGB != 0 {
		diskSizeGb = int(*input.DiskSizeGB)
	}

	var name string
	if input.Name != nil {
		name = *input.Name
	}

	diskEncryptionSetId := ""
	storageAccountType := ""
	secureVMDiskEncryptionSetId := ""
	securityEncryptionType := ""

	if input.ManagedDisk != nil {
		storageAccountType = string(pointer.From(input.ManagedDisk.StorageAccountType))

		if input.ManagedDisk.Id != nil {
			id, err := disks.ParseDiskID(*input.ManagedDisk.Id)
			if err != nil {
				return nil, err
			}

			disk, err := disksClient.Get(ctx, *id)
			if err != nil {
				// turns out ephemeral disks aren't returned/available here
				if !response.WasNotFound(disk.HttpResponse) {
					return nil, err
				}
			}

			// Ephemeral Disks get an ARM ID but aren't available via the regular API
			// ergo fingers crossed we've got it from the resource because ¯\_(ツ)_/¯
			// where else we'd be able to pull it from
			if !response.WasNotFound(disk.HttpResponse) {
				// whilst this is available as `input.ManagedDisk.StorageAccountType` it's not returned there
				// however it's only available there for ephemeral os disks
				if disk.Model.Sku != nil && storageAccountType == "" {
					storageAccountType = string(*disk.Model.Sku.Name)
				}

				// same goes for Disk Size GB apparently
				if diskSizeGb == 0 && disk.Model.Properties != nil && disk.Model.Properties.DiskSizeGB != nil {
					diskSizeGb = int(*disk.Model.Properties.DiskSizeGB)
				}

				// same goes for Disk Encryption Set Id apparently
				if disk.Model.Properties.Encryption != nil && disk.Model.Properties.Encryption.DiskEncryptionSetId != nil {
					diskEncryptionSetId = *disk.Model.Properties.Encryption.DiskEncryptionSetId
				}
			}
		}

		if securityProfile := input.ManagedDisk.SecurityProfile; securityProfile != nil {
			securityEncryptionType = string(pointer.From(securityProfile.SecurityEncryptionType))
			if securityProfile.DiskEncryptionSet != nil && securityProfile.DiskEncryptionSet.Id != nil {
				secureVMDiskEncryptionSetId = *securityProfile.DiskEncryptionSet.Id
			}
		}
	}

	writeAcceleratorEnabled := false
	if input.WriteAcceleratorEnabled != nil {
		writeAcceleratorEnabled = *input.WriteAcceleratorEnabled
	}
	return []interface{}{
		map[string]interface{}{
			"caching":                          string(pointer.From(input.Caching)),
			"disk_size_gb":                     diskSizeGb,
			"diff_disk_settings":               diffDiskSettings,
			"disk_encryption_set_id":           diskEncryptionSetId,
			"name":                             name,
			"storage_account_type":             storageAccountType,
			"secure_vm_disk_encryption_set_id": secureVMDiskEncryptionSetId,
			"security_encryption_type":         securityEncryptionType,
			"write_accelerator_enabled":        writeAcceleratorEnabled,
		},
	}, nil
}

func virtualMachineTerminationNotificationSchema() *pluginsdk.Schema {
	return &pluginsdk.Schema{
		Type:     pluginsdk.TypeList,
		Optional: true,
		Computed: true,
		MaxItems: 1,
		Elem: &pluginsdk.Resource{
			Schema: map[string]*pluginsdk.Schema{
				"enabled": {
					Type:     pluginsdk.TypeBool,
					Required: true,
				},
				"timeout": {
					Type:         pluginsdk.TypeString,
					Optional:     true,
					ValidateFunc: azValidate.ISO8601DurationBetween("PT5M", "PT15M"),
					Default:      "PT5M",
				},
			},
		},
	}
}

func expandVirtualMachineScheduledEventsProfile(input []interface{}) *virtualmachines.ScheduledEventsProfile {
	if len(input) == 0 {
		return &virtualmachines.ScheduledEventsProfile{
			TerminateNotificationProfile: &virtualmachines.TerminateNotificationProfile{
				Enable: utils.Bool(false),
			},
		}
	}

	raw := input[0].(map[string]interface{})
	enabled := raw["enabled"].(bool)
	timeout := raw["timeout"].(string)

	return &virtualmachines.ScheduledEventsProfile{
		TerminateNotificationProfile: &virtualmachines.TerminateNotificationProfile{
			Enable:           &enabled,
			NotBeforeTimeout: &timeout,
		},
	}
}

func flattenVirtualMachineScheduledEventsProfile(input *virtualmachines.ScheduledEventsProfile) []interface{} {
	// if enabled is set to false, there will be no ScheduledEventsProfile in response, to avoid plan non empty when
	// a user explicitly set enabled to false, we need to assign a default block to this field

	enabled := false
	if input != nil && input.TerminateNotificationProfile != nil && input.TerminateNotificationProfile.Enable != nil {
		enabled = *input.TerminateNotificationProfile.Enable
	}

	timeout := "PT5M"
	if input != nil && input.TerminateNotificationProfile != nil && input.TerminateNotificationProfile.NotBeforeTimeout != nil {
		timeout = *input.TerminateNotificationProfile.NotBeforeTimeout
	}

	return []interface{}{
		map[string]interface{}{
			"enabled": enabled,
			"timeout": timeout,
		},
	}
}

func VirtualMachineGalleryApplicationSchema() *pluginsdk.Schema {
	return &pluginsdk.Schema{
		Type:     pluginsdk.TypeList,
		Optional: true,
		MaxItems: 100,
		Elem: &pluginsdk.Resource{
			Schema: map[string]*pluginsdk.Schema{
				"version_id": {
					Type:         pluginsdk.TypeString,
					Required:     true,
					ValidateFunc: galleryapplicationversions.ValidateApplicationVersionID,
				},

				// Example: https://mystorageaccount.blob.core.windows.net/configurations/settings.config
				"configuration_blob_uri": {
					Type:         pluginsdk.TypeString,
					Optional:     true,
					ValidateFunc: validation.IsURLWithHTTPorHTTPS,
				},

				"order": {
					Type:         pluginsdk.TypeInt,
					Optional:     true,
					Default:      0,
					ValidateFunc: validation.IntBetween(0, 2147483647),
				},

				// NOTE: Per the service team, "this is a pass through value that we just add to the model but don't depend on. It can be any string."
				"tag": {
					Type:         pluginsdk.TypeString,
					Optional:     true,
					ValidateFunc: validation.StringIsNotEmpty,
				},
			},
		},
	}
}

func expandVirtualMachineGalleryApplication(input []interface{}) *[]virtualmachines.VMGalleryApplication {
	out := make([]virtualmachines.VMGalleryApplication, 0)
	if len(input) == 0 {
		return &out
	}

	for _, v := range input {
		packageReferenceId := v.(map[string]interface{})["version_id"].(string)
		configurationReference := v.(map[string]interface{})["configuration_blob_uri"].(string)
		order := v.(map[string]interface{})["order"].(int)
		tag := v.(map[string]interface{})["tag"].(string)

		app := &virtualmachines.VMGalleryApplication{
			PackageReferenceId:     packageReferenceId,
			ConfigurationReference: utils.String(configurationReference),
			Order:                  utils.Int64(int64(order)),
			Tags:                   utils.String(tag),
		}

		out = append(out, *app)
	}

	return &out
}

func flattenVirtualMachineGalleryApplication(input *[]virtualmachines.VMGalleryApplication) []interface{} {
	if len(*input) == 0 {
		return nil
	}

	out := make([]interface{}, 0)

	for _, v := range *input {
		var configurationReference, tag string
		var order int

		if v.ConfigurationReference != nil {
			configurationReference = *v.ConfigurationReference
		}

		if v.Order != nil {
			order = int(*v.Order)
		}

		if v.Tags != nil {
			tag = *v.Tags
		}

		app := map[string]interface{}{
			"version_id":             v.PackageReferenceId,
			"configuration_blob_uri": configurationReference,
			"order":                  order,
			"tag":                    tag,
		}

		out = append(out, app)
	}

	return out
}
