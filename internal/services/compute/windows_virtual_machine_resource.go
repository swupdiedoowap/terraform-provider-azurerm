// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

package compute

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/go-azure-helpers/lang/pointer"
	"github.com/hashicorp/go-azure-helpers/lang/response"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/commonschema"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/identity"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/location"
	"github.com/hashicorp/go-azure-helpers/resourcemanager/tags"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2021-11-01/availabilitysets"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2021-11-01/dedicatedhostgroups"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2021-11-01/dedicatedhosts"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2022-03-01/capacityreservationgroups"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2022-03-01/images"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2022-03-01/proximityplacementgroups"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2022-03-02/disks"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2023-03-01/virtualmachines"
	"github.com/hashicorp/go-azure-sdk/resource-manager/compute/2023-03-01/virtualmachinescalesets"
	"github.com/hashicorp/terraform-provider-azurerm/helpers/azure"
	"github.com/hashicorp/terraform-provider-azurerm/helpers/tf"
	azValidate "github.com/hashicorp/terraform-provider-azurerm/helpers/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/clients"
	"github.com/hashicorp/terraform-provider-azurerm/internal/locks"
	computeValidate "github.com/hashicorp/terraform-provider-azurerm/internal/services/compute/validate"
	networkValidate "github.com/hashicorp/terraform-provider-azurerm/internal/services/network/validate"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/base64"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/pluginsdk"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/suppress"
	"github.com/hashicorp/terraform-provider-azurerm/internal/tf/validation"
	"github.com/hashicorp/terraform-provider-azurerm/internal/timeouts"
	"github.com/hashicorp/terraform-provider-azurerm/utils"
)

func resourceWindowsVirtualMachine() *pluginsdk.Resource {
	return &pluginsdk.Resource{
		Create: resourceWindowsVirtualMachineCreate,
		Read:   resourceWindowsVirtualMachineRead,
		Update: resourceWindowsVirtualMachineUpdate,
		Delete: resourceWindowsVirtualMachineDelete,

		Importer: pluginsdk.ImporterValidatingResourceIdThen(func(id string) error {
			_, err := virtualmachines.ParseVirtualMachineID(id)
			return err
		}, importVirtualMachine(virtualmachines.OperatingSystemTypesWindows, "azurerm_windows_virtual_machine")),

		Timeouts: &pluginsdk.ResourceTimeout{
			Create: pluginsdk.DefaultTimeout(45 * time.Minute),
			Read:   pluginsdk.DefaultTimeout(5 * time.Minute),
			Update: pluginsdk.DefaultTimeout(45 * time.Minute),
			Delete: pluginsdk.DefaultTimeout(45 * time.Minute),
		},

		Schema: map[string]*pluginsdk.Schema{
			"name": {
				Type:         pluginsdk.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: computeValidate.VirtualMachineName,
			},

			"resource_group_name": commonschema.ResourceGroupName(),

			"location": commonschema.Location(),

			// Required
			"admin_password": {
				Type:             pluginsdk.TypeString,
				Required:         true,
				ForceNew:         true,
				Sensitive:        true,
				DiffSuppressFunc: adminPasswordDiffSuppressFunc,
				ValidateFunc:     computeValidate.WindowsAdminPassword,
			},

			"admin_username": {
				Type:         pluginsdk.TypeString,
				Required:     true,
				ForceNew:     true,
				ValidateFunc: computeValidate.WindowsAdminUsername,
			},

			"network_interface_ids": {
				Type:     pluginsdk.TypeList,
				Required: true,
				MinItems: 1,
				Elem: &pluginsdk.Schema{
					Type:         pluginsdk.TypeString,
					ValidateFunc: networkValidate.NetworkInterfaceID,
				},
			},

			"os_disk": virtualMachineOSDiskSchema(),

			"size": {
				Type:         pluginsdk.TypeString,
				Required:     true,
				ValidateFunc: validation.StringIsNotEmpty,
			},

			// Optional
			"additional_capabilities": virtualMachineAdditionalCapabilitiesSchema(),

			"additional_unattend_content": additionalUnattendContentSchema(),

			"allow_extension_operations": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				Default:  true,
			},

			"availability_set_id": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: availabilitysets.ValidateAvailabilitySetID,
				// the Compute/VM API is broken and returns the Availability Set name in UPPERCASE :shrug:
				// tracked by https://github.com/Azure/azure-rest-api-specs/issues/19424
				DiffSuppressFunc: suppress.CaseDifference,
				ConflictsWith: []string{
					"capacity_reservation_group_id",
					"virtual_machine_scale_set_id",
					"zone",
				},
			},

			"boot_diagnostics": bootDiagnosticsSchema(),

			"bypass_platform_safety_checks_on_user_schedule_enabled": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				Default:  false,
			},

			"capacity_reservation_group_id": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				// the Compute/VM API is broken and returns the Resource Group name in UPPERCASE
				// tracked by https://github.com/Azure/azure-rest-api-specs/issues/19424
				DiffSuppressFunc: suppress.CaseDifference,
				ValidateFunc:     capacityreservationgroups.ValidateCapacityReservationGroupID,
				ConflictsWith: []string{
					"availability_set_id",
					"proximity_placement_group_id",
				},
			},

			"computer_name": {
				Type:     pluginsdk.TypeString,
				Optional: true,

				// Computed since we reuse the VM name if one's not specified
				Computed: true,
				ForceNew: true,

				ValidateFunc: computeValidate.WindowsComputerNameFull,
			},

			"custom_data": base64.OptionalSchema(true),

			"dedicated_host_id": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				ValidateFunc: dedicatedhosts.ValidateHostID,
				// the Compute/VM API is broken and returns the Resource Group name in UPPERCASE :shrug:
				// tracked by https://github.com/Azure/azure-rest-api-specs/issues/19424
				DiffSuppressFunc: suppress.CaseDifference,
				ConflictsWith: []string{
					"dedicated_host_group_id",
				},
			},

			"dedicated_host_group_id": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				ValidateFunc: dedicatedhostgroups.ValidateHostGroupID,
				// the Compute/VM API is broken and returns the Resource Group name in UPPERCASE
				// tracked by https://github.com/Azure/azure-rest-api-specs/issues/19424
				DiffSuppressFunc: suppress.CaseDifference,
				ConflictsWith: []string{
					"dedicated_host_id",
				},
			},

			"edge_zone": commonschema.EdgeZoneOptionalForceNew(),

			// TODO 4.0: change this from enable_* to *_enabled
			"enable_automatic_updates": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				ForceNew: true, // updating this is not allowed "Changing property 'windowsConfiguration.enableAutomaticUpdates' is not allowed." Target="windowsConfiguration.enableAutomaticUpdates"
				Default:  true,
			},

			"encryption_at_host_enabled": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
			},

			"eviction_policy": {
				// only applicable when `priority` is set to `Spot`
				Type:     pluginsdk.TypeString,
				Optional: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(virtualmachines.VirtualMachineEvictionPolicyTypesDeallocate),
					string(virtualmachines.VirtualMachineEvictionPolicyTypesDelete),
				}, false),
			},

			"extensions_time_budget": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				Default:      "PT1H30M",
				ValidateFunc: azValidate.ISO8601DurationBetween("PT15M", "PT2H"),
			},

			"gallery_application": VirtualMachineGalleryApplicationSchema(),

			"identity": commonschema.SystemAssignedUserAssignedIdentityOptional(),

			"license_type": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				ValidateFunc: validation.StringInSlice([]string{
					"None",
					"Windows_Client",
					"Windows_Server",
				}, false),
				DiffSuppressFunc: func(_, old, new string, _ *pluginsdk.ResourceData) bool {
					if old == "None" && new == "" || old == "" && new == "None" {
						return true
					}

					return false
				},
			},

			"max_bid_price": {
				Type:         pluginsdk.TypeFloat,
				Optional:     true,
				Default:      -1,
				ValidateFunc: validation.FloatAtLeast(-1.0),
			},

			"patch_mode": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				Default:  string(virtualmachines.WindowsVMGuestPatchModeAutomaticByOS),
				ValidateFunc: validation.StringInSlice([]string{
					string(virtualmachines.WindowsVMGuestPatchModeAutomaticByOS),
					string(virtualmachines.WindowsVMGuestPatchModeAutomaticByPlatform),
					string(virtualmachines.WindowsVMGuestPatchModeManual),
				}, false),
			},

			"patch_assessment_mode": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				Default:  string(virtualmachines.WindowsPatchAssessmentModeImageDefault),
				ValidateFunc: validation.StringInSlice([]string{
					string(virtualmachines.WindowsPatchAssessmentModeAutomaticByPlatform),
					string(virtualmachines.WindowsPatchAssessmentModeImageDefault),
				}, false),
			},

			"hotpatching_enabled": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				Default:  false,
			},

			"plan": planSchema(),

			"priority": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				ForceNew: true,
				Default:  string(virtualmachines.VirtualMachinePriorityTypesRegular),
				ValidateFunc: validation.StringInSlice([]string{
					string(virtualmachines.VirtualMachinePriorityTypesRegular),
					string(virtualmachines.VirtualMachinePriorityTypesSpot),
				}, false),
			},

			"provision_vm_agent": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				Default:  true,
				ForceNew: true,
			},

			"proximity_placement_group_id": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				ValidateFunc: proximityplacementgroups.ValidateProximityPlacementGroupID,
				// the Compute/VM API is broken and returns the Resource Group name in UPPERCASE :shrug:
				// tracked by https://github.com/Azure/azure-rest-api-specs/issues/19424
				DiffSuppressFunc: suppress.CaseDifference,
				ConflictsWith: []string{
					"capacity_reservation_group_id",
				},
			},

			"reboot_setting": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				ValidateFunc: validation.StringInSlice([]string{
					string(virtualmachines.WindowsVMGuestPatchAutomaticByPlatformRebootSettingAlways),
					string(virtualmachines.WindowsVMGuestPatchAutomaticByPlatformRebootSettingIfRequired),
					string(virtualmachines.WindowsVMGuestPatchAutomaticByPlatformRebootSettingNever),
				}, false),
			},

			"secret": windowsSecretSchema(),

			"secure_boot_enabled": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				ForceNew: true,
			},

			"source_image_id": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				ForceNew: true,
				ValidateFunc: validation.Any(
					images.ValidateImageID,
					computeValidate.SharedImageID,
					computeValidate.SharedImageVersionID,
					computeValidate.CommunityGalleryImageID,
					computeValidate.CommunityGalleryImageVersionID,
					computeValidate.SharedGalleryImageID,
					computeValidate.SharedGalleryImageVersionID,
				),
				ExactlyOneOf: []string{
					"source_image_id",
					"source_image_reference",
				},
			},

			"source_image_reference": sourceImageReferenceSchema(true),

			"tags": commonschema.Tags(),

			"termination_notification": virtualMachineTerminationNotificationSchema(),

			"timezone": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				ForceNew:     true,
				ValidateFunc: computeValidate.VirtualMachineTimeZone(),
			},

			"virtual_machine_scale_set_id": {
				Type:     pluginsdk.TypeString,
				Optional: true,
				ForceNew: true,
				ConflictsWith: []string{
					"availability_set_id",
				},
				ValidateFunc: virtualmachinescalesets.ValidateVirtualMachineScaleSetID,
			},

			"platform_fault_domain": {
				Type:         pluginsdk.TypeInt,
				Optional:     true,
				Default:      -1,
				ForceNew:     true,
				RequiredWith: []string{"virtual_machine_scale_set_id"},
				ValidateFunc: validation.IntAtLeast(-1),
			},

			"user_data": {
				Type:         pluginsdk.TypeString,
				Optional:     true,
				ValidateFunc: validation.StringIsBase64,
			},

			"vtpm_enabled": {
				Type:     pluginsdk.TypeBool,
				Optional: true,
				ForceNew: true,
			},

			"winrm_listener": winRmListenerSchema(),

			"zone": commonschema.ZoneSingleOptionalForceNew(),

			// Computed
			"private_ip_address": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
			"private_ip_addresses": {
				Type:     pluginsdk.TypeList,
				Computed: true,
				Elem: &pluginsdk.Schema{
					Type: pluginsdk.TypeString,
				},
			},
			"public_ip_address": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
			"public_ip_addresses": {
				Type:     pluginsdk.TypeList,
				Computed: true,
				Elem: &pluginsdk.Schema{
					Type: pluginsdk.TypeString,
				},
			},
			"virtual_machine_id": {
				Type:     pluginsdk.TypeString,
				Computed: true,
			},
		},
	}
}

func resourceWindowsVirtualMachineCreate(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Compute.VMClient
	subscriptionId := meta.(*clients.Client).Account.SubscriptionId
	ctx, cancel := timeouts.ForCreate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id := virtualmachines.NewVirtualMachineID(subscriptionId, d.Get("resource_group_name").(string), d.Get("name").(string))

	locks.ByName(id.VirtualMachineName, VirtualMachineResourceName)
	defer locks.UnlockByName(id.VirtualMachineName, VirtualMachineResourceName)

	resp, err := client.Get(ctx, id, virtualmachines.GetOperationOptions{Expand: pointer.To(virtualmachines.InstanceViewTypesUserData)})
	if err != nil {
		if !response.WasNotFound(resp.HttpResponse) {
			return fmt.Errorf("checking for existing Windows %s: %+v", id, err)
		}
	}

	if !response.WasNotFound(resp.HttpResponse) {
		return tf.ImportAsExistsError("azurerm_windows_virtual_machine", id.ID())
	}

	adminPassword := d.Get("admin_password").(string)
	adminUsername := d.Get("admin_username").(string)
	allowExtensionOperations := d.Get("allow_extension_operations").(bool)

	bootDiagnosticsRaw := d.Get("boot_diagnostics").([]interface{})
	bootDiagnostics := expandBootDiagnostics(bootDiagnosticsRaw)

	var computerName string
	if v, ok := d.GetOk("computer_name"); ok && len(v.(string)) > 0 {
		computerName = v.(string)
	} else {
		_, errs := computeValidate.WindowsComputerNameFull(d.Get("name"), "computer_name")
		if len(errs) > 0 {
			return fmt.Errorf("unable to assume default computer name %s. Please adjust the %q, or specify an explicit %q", errs[0], "name", "computer_name")
		}
		computerName = id.VirtualMachineName
	}

	identity, err := identity.ExpandSystemAndUserAssignedMap(d.Get("identity").([]interface{}))
	if err != nil {
		return fmt.Errorf("expanding `identity`: %+v", err)
	}

	priority := virtualmachines.VirtualMachinePriorityTypes(d.Get("priority").(string))
	provisionVMAgent := d.Get("provision_vm_agent").(bool)
	patchMode := d.Get("patch_mode").(string)
	assessmentMode := d.Get("patch_assessment_mode").(string)
	hotPatch := d.Get("hotpatching_enabled").(bool)
	osDiskRaw := d.Get("os_disk").([]interface{})
	osDisk, err := expandVirtualMachineOSDisk(osDiskRaw, virtualmachines.OperatingSystemTypesWindows)
	if err != nil {
		return fmt.Errorf("expanding `os_disk`: %+v", err)
	}
	securityEncryptionType := osDiskRaw[0].(map[string]interface{})["security_encryption_type"].(string)

	sourceImageReferenceRaw := d.Get("source_image_reference").([]interface{})
	sourceImageId := d.Get("source_image_id").(string)
	sourceImageReference := expandSourceImageReference(sourceImageReferenceRaw, sourceImageId)

	params := virtualmachines.VirtualMachine{
		Name:             utils.String(id.VirtualMachineName),
		ExtendedLocation: expandEdgeZone(d.Get("edge_zone").(string)),
		Location:         azure.NormalizeLocation(d.Get("location").(string)),
		Identity:         identity,
		Plan:             expandPlan(d.Get("plan").([]interface{})),
		Properties: &virtualmachines.VirtualMachineProperties{
			ApplicationProfile: &virtualmachines.ApplicationProfile{
				GalleryApplications: expandVirtualMachineGalleryApplication(d.Get("gallery_application").([]interface{})),
			},
			HardwareProfile: &virtualmachines.HardwareProfile{
				VMSize: pointer.To(virtualmachines.VirtualMachineSizeTypes(d.Get("size").(string))),
			},
			OsProfile: &virtualmachines.OSProfile{
				AdminPassword:            pointer.To(adminPassword),
				AdminUsername:            pointer.To(adminUsername),
				ComputerName:             pointer.To(computerName),
				AllowExtensionOperations: pointer.To(allowExtensionOperations),
				WindowsConfiguration: &virtualmachines.WindowsConfiguration{
					ProvisionVMAgent:       pointer.To(provisionVMAgent),
					EnableAutomaticUpdates: pointer.To(d.Get("enable_automatic_updates").(bool)),
					WinRM:                  expandWinRMListener(d.Get("winrm_listener").(*pluginsdk.Set).List()),
				},
				Secrets: expandWindowsSecrets(d.Get("secret").([]interface{})),
			},
			NetworkProfile: &virtualmachines.NetworkProfile{
				NetworkInterfaces: expandVirtualMachineNetworkInterfaceIDs(d.Get("network_interface_ids").([]interface{})),
			},
			Priority: pointer.To(priority),
			StorageProfile: &virtualmachines.StorageProfile{
				ImageReference: sourceImageReference,
				OsDisk:         osDisk,

				// Data Disks are instead handled via the Association resource - as such we can send an empty value here
				// but for Updates this'll need to be nil, else any associations will be overwritten
				DataDisks: &[]virtualmachines.DataDisk{},
			},

			// Optional
			AdditionalCapabilities: expandVirtualMachineAdditionalCapabilities(d.Get("additional_capabilities").([]interface{})),
			DiagnosticsProfile:     bootDiagnostics,
			ExtensionsTimeBudget:   utils.String(d.Get("extensions_time_budget").(string)),
		},
		Tags: tags.Expand(d.Get("tags").(map[string]interface{})),
	}

	if !provisionVMAgent && allowExtensionOperations {
		return fmt.Errorf("`allow_extension_operations` cannot be set to `true` when `provision_vm_agent` is set to `false`")
	}

	additionalUnattendContent := expandAdditionalUnattendContent(d.Get("additional_unattend_content").([]interface{}))
	if additionalUnattendContent != nil {
		params.Properties.OsProfile.WindowsConfiguration.AdditionalUnattendContent = additionalUnattendContent
	}

	isHotpatchImage := isValidHotPatchSourceImageReference(sourceImageReferenceRaw, sourceImageId)

	// Validate VM Guest Patch Mode configuration
	if patchMode == string(virtualmachines.WindowsVMGuestPatchModeAutomaticByPlatform) && !provisionVMAgent {
		return fmt.Errorf("%q cannot be set to %q when %q is set to %q", "patch_mode", "AutomaticByPlatform", "provision_vm_agent", "false")
	}

	if assessmentMode == string(virtualmachines.WindowsPatchAssessmentModeAutomaticByPlatform) && !provisionVMAgent {
		return fmt.Errorf("`provision_vm_agent` must be set to `true` when `patch_assessment_mode` is set to `AutomaticByPlatform`")
	}

	if isHotpatchImage && patchMode != string(virtualmachines.WindowsVMGuestPatchModeAutomaticByPlatform) {
		return fmt.Errorf("%q must always be set to %q when %q points to a hotpatch enabled image", "patch_mode", "AutomaticByPlatform", "source_image_reference")
	}

	// hot patching can only be enabled if the patch_mode is set to "AutomaticByPlatform"
	// and if the image reference is using one of the following skus:
	// 2022-datacenter-azure-edition-core or 2022-datacenter-azure-edition-core-smalldisk
	if hotPatch {
		if patchMode != string(virtualmachines.WindowsVMGuestPatchModeAutomaticByPlatform) {
			return fmt.Errorf("%q cannot be set to %q when %q is set to %q", "hotpatching_enabled", "true", "patch_mode", patchMode)
		}

		if !provisionVMAgent {
			return fmt.Errorf("%q cannot be set to %q when %q is set to %q", "hotpatching_enabled", "true", "provisionVMAgent", "false")
		}

		if !isHotpatchImage {
			if sourceImageId != "" {
				return fmt.Errorf("the %q field is not supported if referencing the image via the %q field", "hotpatching_enabled", "source_image_id")
			}

			return fmt.Errorf("%q is currently only supported on %q or %q image reference skus", "hotpatching_enabled", "2022-datacenter-azure-edition-core", "2022-datacenter-azure-edition-core-smalldisk")
		}
	}

	params.Properties.OsProfile.WindowsConfiguration.PatchSettings = &virtualmachines.PatchSettings{
		PatchMode:         pointer.To(virtualmachines.WindowsVMGuestPatchMode(patchMode)),
		EnableHotpatching: utils.Bool(hotPatch),
		AssessmentMode:    pointer.To(virtualmachines.WindowsPatchAssessmentMode(assessmentMode)),
	}

	if d.Get("bypass_platform_safety_checks_on_user_schedule_enabled").(bool) {
		if patchMode != string(virtualmachines.WindowsVMGuestPatchModeAutomaticByPlatform) {
			return fmt.Errorf("`patch_mode` must be set to `AutomaticByPlatform` when `bypass_platform_safety_checks_on_user_schedule_enabled` is set to `true`")
		}

		if params.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings == nil {
			params.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings = &virtualmachines.WindowsVMGuestPatchAutomaticByPlatformSettings{}
		}

		params.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings.BypassPlatformSafetyChecksOnUserSchedule = pointer.To(true)
	}

	if v, ok := d.GetOk("reboot_setting"); ok {
		if patchMode != string(virtualmachines.WindowsVMGuestPatchModeAutomaticByPlatform) {
			return fmt.Errorf("`patch_mode` must be set to `AutomaticByPlatform` when `reboot_setting` is specified")
		}

		if params.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings == nil {
			params.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings = &virtualmachines.WindowsVMGuestPatchAutomaticByPlatformSettings{}
		}

		params.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings.RebootSetting = pointer.To(virtualmachines.WindowsVMGuestPatchAutomaticByPlatformRebootSetting(v.(string)))
	}

	if v, ok := d.GetOk("availability_set_id"); ok {
		params.Properties.AvailabilitySet = &virtualmachines.SubResource{
			Id: utils.String(v.(string)),
		}
	}

	if v, ok := d.GetOk("capacity_reservation_group_id"); ok {
		params.Properties.CapacityReservation = &virtualmachines.CapacityReservationProfile{
			CapacityReservationGroup: &virtualmachines.SubResource{
				Id: utils.String(v.(string)),
			},
		}
	}

	if v, ok := d.GetOk("custom_data"); ok {
		params.Properties.OsProfile.CustomData = utils.String(v.(string))
	}

	if v, ok := d.GetOk("dedicated_host_id"); ok {
		params.Properties.Host = &virtualmachines.SubResource{
			Id: utils.String(v.(string)),
		}
	}

	if v, ok := d.GetOk("dedicated_host_group_id"); ok {
		params.Properties.HostGroup = &virtualmachines.SubResource{
			Id: utils.String(v.(string)),
		}
	}

	if encryptionAtHostEnabled, ok := d.GetOk("encryption_at_host_enabled"); ok {
		if encryptionAtHostEnabled.(bool) {
			if virtualmachines.SecurityEncryptionTypesDiskWithVMGuestState == virtualmachines.SecurityEncryptionTypes(securityEncryptionType) {
				return fmt.Errorf("`encryption_at_host_enabled` cannot be set to `true` when `os_disk.0.security_encryption_type` is set to `DiskWithVMGuestState`")
			}
		}

		if params.Properties.SecurityProfile == nil {
			params.Properties.SecurityProfile = &virtualmachines.SecurityProfile{}
		}
		params.Properties.SecurityProfile.EncryptionAtHost = utils.Bool(encryptionAtHostEnabled.(bool))
	}

	secureBootEnabled := d.Get("secure_boot_enabled").(bool)
	vtpmEnabled := d.Get("vtpm_enabled").(bool)
	if securityEncryptionType != "" {
		if virtualmachines.SecurityEncryptionTypesDiskWithVMGuestState == virtualmachines.SecurityEncryptionTypes(securityEncryptionType) && !secureBootEnabled {
			return fmt.Errorf("`secure_boot_enabled` must be set to `true` when `os_disk.0.security_encryption_type` is set to `DiskWithVMGuestState`")
		}
		if !vtpmEnabled {
			return fmt.Errorf("`vtpm_enabled` must be set to `true` when `os_disk.0.security_encryption_type` is set")
		}

		if params.Properties.SecurityProfile == nil {
			params.Properties.SecurityProfile = &virtualmachines.SecurityProfile{}
		}
		params.Properties.SecurityProfile.SecurityType = pointer.To(virtualmachines.SecurityTypesConfidentialVM)

		if params.Properties.SecurityProfile.UefiSettings == nil {
			params.Properties.SecurityProfile.UefiSettings = &virtualmachines.UefiSettings{}
		}
		params.Properties.SecurityProfile.UefiSettings.SecureBootEnabled = utils.Bool(secureBootEnabled)
		params.Properties.SecurityProfile.UefiSettings.VTpmEnabled = utils.Bool(vtpmEnabled)
	} else {
		if secureBootEnabled {
			if params.Properties.SecurityProfile == nil {
				params.Properties.SecurityProfile = &virtualmachines.SecurityProfile{}
			}
			if params.Properties.SecurityProfile.UefiSettings == nil {
				params.Properties.SecurityProfile.UefiSettings = &virtualmachines.UefiSettings{}
			}
			params.Properties.SecurityProfile.SecurityType = pointer.To(virtualmachines.SecurityTypesTrustedLaunch)
			params.Properties.SecurityProfile.UefiSettings.SecureBootEnabled = utils.Bool(secureBootEnabled)
		}

		if vtpmEnabled {
			if params.Properties.SecurityProfile == nil {
				params.Properties.SecurityProfile = &virtualmachines.SecurityProfile{}
			}
			if params.Properties.SecurityProfile.UefiSettings == nil {
				params.Properties.SecurityProfile.UefiSettings = &virtualmachines.UefiSettings{}
			}
			params.Properties.SecurityProfile.SecurityType = pointer.To(virtualmachines.SecurityTypesTrustedLaunch)
			params.Properties.SecurityProfile.UefiSettings.VTpmEnabled = utils.Bool(vtpmEnabled)
		}
	}

	if evictionPolicyRaw, ok := d.GetOk("eviction_policy"); ok {
		if pointer.From(params.Properties.Priority) != virtualmachines.VirtualMachinePriorityTypesSpot {
			return fmt.Errorf("an `eviction_policy` can only be specified when `priority` is set to `Spot`")
		}

		params.Properties.EvictionPolicy = pointer.To(virtualmachines.VirtualMachineEvictionPolicyTypes(evictionPolicyRaw.(string)))
	} else if priority == virtualmachines.VirtualMachinePriorityTypesSpot {
		return fmt.Errorf("an `eviction_policy` must be specified when `priority` is set to `Spot`")
	}

	if v, ok := d.GetOk("license_type"); ok {
		params.Properties.LicenseType = utils.String(v.(string))
	}

	if v, ok := d.Get("max_bid_price").(float64); ok && v > 0 {
		if priority != virtualmachines.VirtualMachinePriorityTypesSpot {
			return fmt.Errorf("`max_bid_price` can only be configured when `priority` is set to `Spot`")
		}

		params.Properties.BillingProfile = &virtualmachines.BillingProfile{
			MaxPrice: utils.Float(v),
		}
	}

	if v, ok := d.GetOk("proximity_placement_group_id"); ok {
		params.Properties.ProximityPlacementGroup = &virtualmachines.SubResource{
			Id: utils.String(v.(string)),
		}
	}

	if v, ok := d.GetOk("virtual_machine_scale_set_id"); ok {
		params.Properties.VirtualMachineScaleSet = &virtualmachines.SubResource{
			Id: utils.String(v.(string)),
		}
	}

	platformFaultDomain := d.Get("platform_fault_domain").(int)
	if platformFaultDomain != -1 {
		params.Properties.PlatformFaultDomain = utils.Int64(int64(platformFaultDomain))
	}

	if v, ok := d.GetOk("termination_notification"); ok {
		params.Properties.ScheduledEventsProfile = expandVirtualMachineScheduledEventsProfile(v.([]interface{}))
	}

	if v, ok := d.GetOk("timezone"); ok {
		params.Properties.OsProfile.WindowsConfiguration.TimeZone = utils.String(v.(string))
	}

	if v, ok := d.GetOk("user_data"); ok {
		params.Properties.UserData = utils.String(v.(string))
	}

	if v, ok := d.GetOk("zone"); ok {
		params.Zones = &[]string{
			v.(string),
		}
	}

	if err = client.CreateOrUpdateThenPoll(ctx, id, params); err != nil {
		return fmt.Errorf("creating Windows %s: %+v", id, err)
	}

	d.SetId(id.ID())
	return resourceWindowsVirtualMachineRead(d, meta)
}

func resourceWindowsVirtualMachineRead(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Compute.VMClient
	disksClient := meta.(*clients.Client).Compute.DisksClient
	networkInterfacesClient := meta.(*clients.Client).Network.InterfacesClient
	publicIPAddressesClient := meta.(*clients.Client).Network.PublicIPsClient
	ctx, cancel := timeouts.ForRead(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := virtualmachines.ParseVirtualMachineID(d.Id())
	if err != nil {
		return err
	}

	resp, err := client.Get(ctx, *id, virtualmachines.GetOperationOptions{Expand: pointer.To(virtualmachines.InstanceViewTypesUserData)})
	if err != nil {
		if response.WasNotFound(resp.HttpResponse) {
			log.Printf("[DEBUG] %s was not found - removing from state!", id)
			d.SetId("")
			return nil
		}

		return fmt.Errorf("retrieving %s: %+v", id, err)
	}

	d.Set("name", id.VirtualMachineName)
	d.Set("resource_group_name", id.ResourceGroupName)

	if model := resp.Model; model != nil {
		d.Set("location", location.Normalize(model.Location))
		d.Set("edge_zone", flattenEdgeZone(model.ExtendedLocation))

		identity, err := identity.FlattenSystemAndUserAssignedMap(model.Identity)
		if err != nil {
			return fmt.Errorf("flattening `identity`: %+v", err)
		}
		if err := d.Set("identity", identity); err != nil {
			return fmt.Errorf("setting `identity`: %+v", err)
		}

		if err := d.Set("plan", flattenPlan(model.Plan)); err != nil {
			return fmt.Errorf("setting `plan`: %+v", err)
		}

		if props := model.Properties; props != nil {
			if err := d.Set("additional_capabilities", flattenVirtualMachineAdditionalCapabilities(props.AdditionalCapabilities)); err != nil {
				return fmt.Errorf("setting `additional_capabilities`: %+v", err)
			}

			availabilitySetId := ""
			if props.AvailabilitySet != nil && props.AvailabilitySet.Id != nil {
				availabilitySetId = *props.AvailabilitySet.Id
			}
			d.Set("availability_set_id", availabilitySetId)

			capacityReservationGroupId := ""
			if props.CapacityReservation != nil && props.CapacityReservation.CapacityReservationGroup != nil && props.CapacityReservation.CapacityReservationGroup.Id != nil {
				capacityReservationGroupId = *props.CapacityReservation.CapacityReservationGroup.Id
			}
			d.Set("capacity_reservation_group_id", capacityReservationGroupId)

			if err := d.Set("boot_diagnostics", flattenBootDiagnostics(props.DiagnosticsProfile)); err != nil {
				return fmt.Errorf("setting `boot_diagnostics`: %+v", err)
			}

			d.Set("eviction_policy", string(pointer.From(props.EvictionPolicy)))
			if profile := props.HardwareProfile; profile != nil {
				d.Set("size", string(pointer.From(profile.VMSize)))
			}
			d.Set("license_type", props.LicenseType)

			extensionsTimeBudget := "PT1H30M"
			if props.ExtensionsTimeBudget != nil {
				extensionsTimeBudget = *props.ExtensionsTimeBudget
			}
			d.Set("extensions_time_budget", extensionsTimeBudget)

			if props.ApplicationProfile != nil && props.ApplicationProfile.GalleryApplications != nil {
				d.Set("gallery_application", flattenVirtualMachineGalleryApplication(props.ApplicationProfile.GalleryApplications))
			}

			// defaulted since BillingProfile isn't returned if it's unset
			maxBidPrice := float64(-1.0)
			if props.BillingProfile != nil && props.BillingProfile.MaxPrice != nil {
				maxBidPrice = *props.BillingProfile.MaxPrice
			}
			d.Set("max_bid_price", maxBidPrice)

			if profile := props.NetworkProfile; profile != nil {
				if err := d.Set("network_interface_ids", flattenVirtualMachineNetworkInterfaceIDs(props.NetworkProfile.NetworkInterfaces)); err != nil {
					return fmt.Errorf("setting `network_interface_ids`: %+v", err)
				}
			}

			dedicatedHostId := ""
			if props.Host != nil && props.Host.Id != nil {
				dedicatedHostId = *props.Host.Id
			}
			d.Set("dedicated_host_id", dedicatedHostId)

			dedicatedHostGroupId := ""
			if props.HostGroup != nil && props.HostGroup.Id != nil {
				dedicatedHostGroupId = *props.HostGroup.Id
			}
			d.Set("dedicated_host_group_id", dedicatedHostGroupId)

			virtualMachineScaleSetId := ""
			if props.VirtualMachineScaleSet != nil && props.VirtualMachineScaleSet.Id != nil {
				virtualMachineScaleSetId = *props.VirtualMachineScaleSet.Id
			}
			d.Set("virtual_machine_scale_set_id", virtualMachineScaleSetId)
			platformFaultDomain := -1
			if props.PlatformFaultDomain != nil {
				platformFaultDomain = int(*props.PlatformFaultDomain)
			}
			d.Set("platform_fault_domain", platformFaultDomain)

			if profile := props.OsProfile; profile != nil {
				d.Set("admin_username", profile.AdminUsername)
				d.Set("allow_extension_operations", profile.AllowExtensionOperations)
				d.Set("computer_name", profile.ComputerName)

				if config := profile.WindowsConfiguration; config != nil {
					if err := d.Set("additional_unattend_content", flattenAdditionalUnattendContent(config.AdditionalUnattendContent, d)); err != nil {
						return fmt.Errorf("setting `additional_unattend_content`: %+v", err)
					}

					d.Set("enable_automatic_updates", config.EnableAutomaticUpdates)

					d.Set("provision_vm_agent", config.ProvisionVMAgent)

					assessmentMode := string(virtualmachines.WindowsPatchAssessmentModeImageDefault)
					bypassPlatformSafetyChecksOnUserScheduleEnabled := false
					rebootSetting := ""
					if patchSettings := config.PatchSettings; patchSettings != nil {
						d.Set("patch_mode", patchSettings.PatchMode)
						d.Set("hotpatching_enabled", patchSettings.EnableHotpatching)

						if patchSettings.AutomaticByPlatformSettings != nil {
							bypassPlatformSafetyChecksOnUserScheduleEnabled = pointer.From(patchSettings.AutomaticByPlatformSettings.BypassPlatformSafetyChecksOnUserSchedule)
							rebootSetting = string(pointer.From(patchSettings.AutomaticByPlatformSettings.RebootSetting))
						}
						if pointer.From(patchSettings.AssessmentMode) != "" {
							assessmentMode = string(pointer.From(patchSettings.AssessmentMode))
						}
					}

					d.Set("patch_assessment_mode", assessmentMode)
					d.Set("bypass_platform_safety_checks_on_user_schedule_enabled", bypassPlatformSafetyChecksOnUserScheduleEnabled)
					d.Set("reboot_setting", rebootSetting)

					d.Set("timezone", config.TimeZone)

					if err := d.Set("winrm_listener", flattenWinRMListener(config.WinRM)); err != nil {
						return fmt.Errorf("setting `winrm_listener`: %+v", err)
					}
				}

				if err := d.Set("secret", flattenWindowsSecrets(profile.Secrets)); err != nil {
					return fmt.Errorf("setting `secret`: %+v", err)
				}
			}
			// Resources created with azurerm_virtual_machine have priority set to ""
			// We need to treat "" as equal to "Regular" to allow migration azurerm_virtual_machine -> azurerm_linux_virtual_machine
			priority := string(virtualmachines.VirtualMachinePriorityTypesRegular)
			if pointer.From(props.Priority) != "" {
				priority = string(pointer.From(props.Priority))
			}
			d.Set("priority", priority)
			proximityPlacementGroupId := ""
			if props.ProximityPlacementGroup != nil && props.ProximityPlacementGroup.Id != nil {
				proximityPlacementGroupId = *props.ProximityPlacementGroup.Id
			}
			d.Set("proximity_placement_group_id", proximityPlacementGroupId)

			if profile := props.StorageProfile; profile != nil {
				// the storage_account_type isn't returned so we need to look it up
				flattenedOSDisk, err := flattenVirtualMachineOSDisk(ctx, disksClient, profile.OsDisk)
				if err != nil {
					return fmt.Errorf("flattening `os_disk`: %+v", err)
				}
				if err := d.Set("os_disk", flattenedOSDisk); err != nil {
					return fmt.Errorf("settings `os_disk`: %+v", err)
				}

				var storageImageId string
				if profile.ImageReference != nil && profile.ImageReference.Id != nil {
					storageImageId = *profile.ImageReference.Id
				}
				if profile.ImageReference != nil && profile.ImageReference.CommunityGalleryImageId != nil {
					storageImageId = *profile.ImageReference.CommunityGalleryImageId
				}
				if profile.ImageReference != nil && profile.ImageReference.SharedGalleryImageId != nil {
					storageImageId = *profile.ImageReference.SharedGalleryImageId
				}
				d.Set("source_image_id", storageImageId)

				if err := d.Set("source_image_reference", flattenSourceImageReference(profile.ImageReference, storageImageId != "")); err != nil {
					return fmt.Errorf("setting `source_image_reference`: %+v", err)
				}
			}

			if scheduleProfile := props.ScheduledEventsProfile; scheduleProfile != nil {
				if err := d.Set("termination_notification", flattenVirtualMachineScheduledEventsProfile(scheduleProfile)); err != nil {
					return fmt.Errorf("setting `termination_notification`: %+v", err)
				}
			}

			encryptionAtHostEnabled := false
			vtpmEnabled := false
			secureBootEnabled := false

			if secprofile := props.SecurityProfile; secprofile != nil {
				if secprofile.EncryptionAtHost != nil {
					encryptionAtHostEnabled = *secprofile.EncryptionAtHost
				}
				if uefi := props.SecurityProfile.UefiSettings; uefi != nil {
					if uefi.VTpmEnabled != nil {
						vtpmEnabled = *uefi.VTpmEnabled
					}
					if uefi.SecureBootEnabled != nil {
						secureBootEnabled = *uefi.SecureBootEnabled
					}
				}
			}

			d.Set("encryption_at_host_enabled", encryptionAtHostEnabled)
			d.Set("vtpm_enabled", vtpmEnabled)
			d.Set("secure_boot_enabled", secureBootEnabled)

			d.Set("virtual_machine_id", props.VMId)

			d.Set("user_data", props.UserData)

			zone := ""
			if model.Zones != nil {
				if zones := *model.Zones; len(zones) > 0 {
					zone = zones[0]
				}
			}
			d.Set("zone", zone)

			connectionInfo := retrieveConnectionInformation(ctx, networkInterfacesClient, publicIPAddressesClient, model.Properties)
			d.Set("private_ip_address", connectionInfo.primaryPrivateAddress)
			d.Set("private_ip_addresses", connectionInfo.privateAddresses)
			d.Set("public_ip_address", connectionInfo.primaryPublicAddress)
			d.Set("public_ip_addresses", connectionInfo.publicAddresses)
			isWindows := false
			setConnectionInformation(d, connectionInfo, isWindows)
		}

		return tags.FlattenAndSet(d, model.Tags)
	}

	return nil
}

func resourceWindowsVirtualMachineUpdate(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Compute.VMClient
	ctx, cancel := timeouts.ForUpdate(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := virtualmachines.ParseVirtualMachineID(d.Id())
	if err != nil {
		return err
	}

	locks.ByName(id.VirtualMachineName, VirtualMachineResourceName)
	defer locks.UnlockByName(id.VirtualMachineName, VirtualMachineResourceName)

	log.Printf("[DEBUG] Retrieving %s..", id.VirtualMachineName, id.ResourceGroupName)
	existing, err := client.Get(ctx, *id, virtualmachines.GetOperationOptions{Expand: pointer.To(virtualmachines.InstanceViewTypesUserData)})
	if err != nil {
		return fmt.Errorf("retrieving %s: %+v", id, err)
	}

	log.Printf("[DEBUG] Retrieving InstanceView for %s", id)
	instanceView, err := client.InstanceView(ctx, *id)
	if err != nil {
		return fmt.Errorf("retrieving InstanceView for %s: %+v", id, err)
	}

	shouldTurnBackOn := virtualMachineShouldBeStarted(instanceView.Model)
	hasEphemeralOSDisk := false
	if model := existing.Model; model != nil {
		if props := existing.Model.Properties; props != nil {
			if storage := props.StorageProfile; storage != nil {
				if disk := storage.OsDisk; disk != nil {
					if settings := disk.DiffDiskSettings; settings != nil {
						hasEphemeralOSDisk = pointer.From(settings.Option) == virtualmachines.DiffDiskOptionsLocal
					}
				}
			}
		}
	}

	shouldUpdate := false
	shouldShutDown := false
	shouldDeallocate := false

	update := virtualmachines.VirtualMachineUpdate{
		Properties: &virtualmachines.VirtualMachineProperties{},
	}

	if d.HasChange("boot_diagnostics") {
		shouldUpdate = true

		bootDiagnosticsRaw := d.Get("boot_diagnostics").([]interface{})
		update.Properties.DiagnosticsProfile = expandBootDiagnostics(bootDiagnosticsRaw)
	}

	if d.HasChange("secret") {
		shouldUpdate = true

		profile := virtualmachines.OSProfile{}

		if d.HasChange("secret") {
			secretsRaw := d.Get("secret").([]interface{})
			profile.Secrets = expandWindowsSecrets(secretsRaw)
		}

		update.Properties.OsProfile = &profile
	}

	if d.HasChange("allow_extension_operations") {
		allowExtensionOperations := d.Get("allow_extension_operations").(bool)

		shouldUpdate = true

		if update.Properties.OsProfile == nil {
			update.Properties.OsProfile = &virtualmachines.OSProfile{}
		}

		update.Properties.OsProfile.AllowExtensionOperations = utils.Bool(allowExtensionOperations)
	}

	if d.HasChange("patch_mode") {
		shouldUpdate = true

		if update.Properties.OsProfile == nil {
			update.Properties.OsProfile = &virtualmachines.OSProfile{}
		}

		if update.Properties.OsProfile.WindowsConfiguration == nil {
			update.Properties.OsProfile.WindowsConfiguration = &virtualmachines.WindowsConfiguration{}
		}

		if update.Properties.OsProfile.WindowsConfiguration.PatchSettings == nil {
			update.Properties.OsProfile.WindowsConfiguration.PatchSettings = &virtualmachines.PatchSettings{}
		}

		update.Properties.OsProfile.WindowsConfiguration.PatchSettings.PatchMode = pointer.To(virtualmachines.WindowsVMGuestPatchMode(d.Get("patch_mode").(string)))
	}

	if d.HasChange("patch_assessment_mode") {
		assessmentMode := d.Get("patch_assessment_mode").(string)
		if assessmentMode == string(virtualmachines.WindowsPatchAssessmentModeAutomaticByPlatform) && !d.Get("provision_vm_agent").(bool) {
			return fmt.Errorf("`provision_vm_agent` must be set to `true` when `patch_assessment_mode` is set to `AutomaticByPlatform`")
		}

		shouldUpdate = true

		if update.Properties.OsProfile == nil {
			update.Properties.OsProfile = &virtualmachines.OSProfile{}
		}

		if update.Properties.OsProfile.WindowsConfiguration == nil {
			update.Properties.OsProfile.WindowsConfiguration = &virtualmachines.WindowsConfiguration{}
		}

		if update.Properties.OsProfile.WindowsConfiguration.PatchSettings == nil {
			update.Properties.OsProfile.WindowsConfiguration.PatchSettings = &virtualmachines.PatchSettings{}
		}

		update.Properties.OsProfile.WindowsConfiguration.PatchSettings.AssessmentMode = pointer.To(virtualmachines.WindowsPatchAssessmentMode(assessmentMode))
	}

	isPatchModeAutomaticByPlatform := d.Get("patch_mode") == string(virtualmachines.WindowsVMGuestPatchModeAutomaticByPlatform)
	bypassPlatformSafetyChecksOnUserScheduleEnabled := d.Get("bypass_platform_safety_checks_on_user_schedule_enabled").(bool)
	if bypassPlatformSafetyChecksOnUserScheduleEnabled && !isPatchModeAutomaticByPlatform {
		return fmt.Errorf("`patch_mode` must be set to `AutomaticByPlatform` when `bypass_platform_safety_checks_on_user_schedule_enabled` is set to `true`")
	}
	if d.HasChange("bypass_platform_safety_checks_on_user_schedule_enabled") {
		shouldUpdate = true

		if update.Properties.OsProfile == nil {
			update.Properties.OsProfile = &virtualmachines.OSProfile{}
		}

		if update.Properties.OsProfile.WindowsConfiguration == nil {
			update.Properties.OsProfile.WindowsConfiguration = &virtualmachines.WindowsConfiguration{}
		}

		if update.Properties.OsProfile.WindowsConfiguration.PatchSettings == nil {
			update.Properties.OsProfile.WindowsConfiguration.PatchSettings = &virtualmachines.PatchSettings{}
		}

		if isPatchModeAutomaticByPlatform {
			if update.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings == nil {
				update.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings = &virtualmachines.WindowsVMGuestPatchAutomaticByPlatformSettings{}
			}

			update.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings.BypassPlatformSafetyChecksOnUserSchedule = pointer.To(bypassPlatformSafetyChecksOnUserScheduleEnabled)
		}
	}

	rebootSetting := d.Get("reboot_setting").(string)
	if rebootSetting != "" && !isPatchModeAutomaticByPlatform {
		return fmt.Errorf("`patch_mode` must be set to `AutomaticByPlatform` when `reboot_setting` is specified")
	}
	if d.HasChange("reboot_setting") {
		shouldUpdate = true

		if update.Properties.OsProfile == nil {
			update.Properties.OsProfile = &virtualmachines.OSProfile{}
		}

		if update.Properties.OsProfile.WindowsConfiguration == nil {
			update.Properties.OsProfile.WindowsConfiguration = &virtualmachines.WindowsConfiguration{}
		}

		if update.Properties.OsProfile.WindowsConfiguration.PatchSettings == nil {
			update.Properties.OsProfile.WindowsConfiguration.PatchSettings = &virtualmachines.PatchSettings{}
		}

		if isPatchModeAutomaticByPlatform {
			if update.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings == nil {
				update.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings = &virtualmachines.WindowsVMGuestPatchAutomaticByPlatformSettings{}
			}

			update.Properties.OsProfile.WindowsConfiguration.PatchSettings.AutomaticByPlatformSettings.RebootSetting = pointer.To(virtualmachines.WindowsVMGuestPatchAutomaticByPlatformRebootSetting(rebootSetting))
		}
	}

	if d.HasChange("hotpatching_enabled") {
		shouldUpdate = true

		if update.Properties.OsProfile == nil {
			update.Properties.OsProfile = &virtualmachines.OSProfile{}
		}

		if update.Properties.OsProfile.WindowsConfiguration == nil {
			update.Properties.OsProfile.WindowsConfiguration = &virtualmachines.WindowsConfiguration{}
		}

		if update.Properties.OsProfile.WindowsConfiguration.PatchSettings == nil {
			update.Properties.OsProfile.WindowsConfiguration.PatchSettings = &virtualmachines.PatchSettings{}
		}

		update.Properties.OsProfile.WindowsConfiguration.PatchSettings.EnableHotpatching = utils.Bool(d.Get("hotpatching_enabled").(bool))
	}

	if d.HasChange("identity") {
		shouldUpdate = true

		identityRaw := d.Get("identity").([]interface{})
		identity, err := identity.ExpandSystemAndUserAssignedMap(identityRaw)
		if err != nil {
			return fmt.Errorf("expanding `identity`: %+v", err)
		}
		update.Identity = identity
	}

	if d.HasChange("capacity_reservation_group_id") {
		shouldUpdate = true
		shouldDeallocate = true

		if v, ok := d.GetOk("capacity_reservation_group_id"); ok {
			update.Properties.CapacityReservation = &virtualmachines.CapacityReservationProfile{
				CapacityReservationGroup: &virtualmachines.SubResource{
					Id: utils.String(v.(string)),
				},
			}
		} else {
			update.Properties.CapacityReservation = &virtualmachines.CapacityReservationProfile{
				CapacityReservationGroup: &virtualmachines.SubResource{},
			}
		}
	}

	if d.HasChange("dedicated_host_id") {
		shouldUpdate = true

		// Code="PropertyChangeNotAllowed" Message="Updating Host of VM 'VMNAME' is not allowed as the VM is currently allocated. Please Deallocate the VM and retry the operation."
		shouldDeallocate = true

		if v, ok := d.GetOk("dedicated_host_id"); ok {
			update.Properties.Host = &virtualmachines.SubResource{
				Id: utils.String(v.(string)),
			}
		} else {
			update.Properties.Host = &virtualmachines.SubResource{}
		}
	}

	if d.HasChange("dedicated_host_group_id") {
		shouldUpdate = true

		// Code="PropertyChangeNotAllowed" Message="Updating Host of VM 'VMNAME' is not allowed as the VM is currently allocated. Please Deallocate the VM and retry the operation."
		shouldDeallocate = true

		if v, ok := d.GetOk("dedicated_host_group_id"); ok {
			update.Properties.HostGroup = &virtualmachines.SubResource{
				Id: utils.String(v.(string)),
			}
		} else {
			update.Properties.HostGroup = &virtualmachines.SubResource{}
		}
	}

	if d.HasChange("extensions_time_budget") {
		shouldUpdate = true
		update.Properties.ExtensionsTimeBudget = utils.String(d.Get("extensions_time_budget").(string))
	}

	if d.HasChange("gallery_application") {
		shouldUpdate = true
		update.Properties.ApplicationProfile = &virtualmachines.ApplicationProfile{
			GalleryApplications: expandVirtualMachineGalleryApplication(d.Get("gallery_application").([]interface{})),
		}
	}

	if d.HasChange("max_bid_price") {
		shouldUpdate = true

		// Code="OperationNotAllowed" Message="Max price change is not allowed. For more information, see http://aka.ms/AzureSpot/errormessages"
		shouldShutDown = true

		// "code":"OperationNotAllowed"
		// "message": "Max price change is not allowed when the VM [name] is currently allocated.
		//			   Please deallocate and try again. For more information, see http://aka.ms/AzureSpot/errormessages"
		shouldDeallocate = true

		maxBidPrice := d.Get("max_bid_price").(float64)
		update.Properties.BillingProfile = &virtualmachines.BillingProfile{
			MaxPrice: utils.Float(maxBidPrice),
		}
	}

	if d.HasChange("network_interface_ids") {
		shouldUpdate = true

		// Code="CannotAddOrRemoveNetworkInterfacesFromARunningVirtualMachine"
		// Message="Secondary network interfaces cannot be added or removed from a running virtual machine.
		shouldShutDown = true

		// @tombuildsstuff: after testing shutting it down isn't sufficient - we need a full deallocation
		shouldDeallocate = true

		update.Properties.NetworkProfile = &virtualmachines.NetworkProfile{
			NetworkInterfaces: expandVirtualMachineNetworkInterfaceIDs(d.Get("network_interface_ids").([]interface{})),
		}
	}

	if d.HasChange("os_disk") {
		shouldUpdate = true

		// Code="Conflict" Message="Disk resizing is allowed only when creating a VM or when the VM is deallocated." Target="disk.diskSizeGB"
		shouldShutDown = true
		shouldDeallocate = true

		osDiskRaw := d.Get("os_disk").([]interface{})
		osDisk, err := expandVirtualMachineOSDisk(osDiskRaw, virtualmachines.OperatingSystemTypesWindows)
		if err != nil {
			return fmt.Errorf("expanding `os_disk`: %+v", err)
		}

		update.Properties.StorageProfile = &virtualmachines.StorageProfile{
			OsDisk: osDisk,
		}
	}

	if d.HasChange("proximity_placement_group_id") {
		shouldUpdate = true

		// Code="OperationNotAllowed" Message="Updating proximity placement group of VM is not allowed while the VM is running. Please stop/deallocate the VM and retry the operation."
		shouldShutDown = true
		shouldDeallocate = true

		if ppgIDRaw, ok := d.GetOk("proximity_placement_group_id"); ok {
			update.Properties.ProximityPlacementGroup = &virtualmachines.SubResource{
				Id: utils.String(ppgIDRaw.(string)),
			}
		} else {
			update.Properties.ProximityPlacementGroup = &virtualmachines.SubResource{}
		}
	}

	if d.HasChange("size") {
		shouldUpdate = true

		// this is kind of superflurious since Azure can do this for us, but if we do this we can subsequently
		// deallocate the VM to switch hosts if required
		shouldShutDown = true
		vmSize := d.Get("size").(string)

		// Azure will auto-reboot this for us, providing this machine will fit on this host
		// otherwise we need to shut down the VM to move it to another host to be able to use this size
		availableOnThisHost := false
		sizes, err := client.ListAvailableSizes(ctx, *id)
		if err != nil {
			return fmt.Errorf("retrieving available sizes for %s: %+v", id, err)
		}

		if model := sizes.Model; model != nil {
			if model.Value != nil {
				for _, size := range *model.Value {
					if size.Name == nil {
						continue
					}

					if strings.EqualFold(*size.Name, vmSize) {
						availableOnThisHost = true
						break
					}
				}
			}
		}

		if !availableOnThisHost {
			log.Printf("[DEBUG] Requested VM Size isn't available on the Host - must switch host to resize..")
			// Code="OperationNotAllowed"
			// Message="Unable to resize the VM [name] because the requested size Standard_F4s_v2 is not available in the current hardware cluster.
			//         The available sizes in this cluster are: [list]. The requested size might be available in other clusters of this region.
			//         Read more on VM resizing strategy at https://aka.ms/azure-resizevm."
			shouldDeallocate = true
		}

		update.Properties.HardwareProfile = &virtualmachines.HardwareProfile{
			VMSize: pointer.To(virtualmachines.VirtualMachineSizeTypes(vmSize)),
		}
	}

	if d.HasChange("tags") {
		shouldUpdate = true

		tagsRaw := d.Get("tags").(map[string]interface{})
		update.Tags = tags.Expand(tagsRaw)
	}

	if d.HasChange("termination_notification") {
		shouldUpdate = true

		notificationRaw := d.Get("termination_notification").([]interface{})
		update.Properties.ScheduledEventsProfile = expandVirtualMachineScheduledEventsProfile(notificationRaw)
	}

	if d.HasChange("additional_capabilities") {
		shouldUpdate = true

		if d.HasChange("additional_capabilities.0.ultra_ssd_enabled") {
			shouldShutDown = true
			shouldDeallocate = true
		}

		additionalCapabilitiesRaw := d.Get("additional_capabilities").([]interface{})
		update.Properties.AdditionalCapabilities = expandVirtualMachineAdditionalCapabilities(additionalCapabilitiesRaw)
	}

	if d.HasChange("encryption_at_host_enabled") {
		if d.Get("encryption_at_host_enabled").(bool) {
			osDiskRaw := d.Get("os_disk").([]interface{})
			securityEncryptionType := osDiskRaw[0].(map[string]interface{})["security_encryption_type"].(string)
			if virtualmachines.SecurityEncryptionTypesDiskWithVMGuestState == virtualmachines.SecurityEncryptionTypes(securityEncryptionType) {
				return fmt.Errorf("`encryption_at_host_enabled` cannot be set to `true` when `os_disk.0.security_encryption_type` is set to `DiskWithVMGuestState`")
			}
		}

		shouldUpdate = true
		shouldDeallocate = true // API returns the following error if not deallocate: 'securityProfile.encryptionAtHost' can be updated only when VM is in deallocated state
		if update.Properties.SecurityProfile == nil {
			update.Properties.SecurityProfile = &virtualmachines.SecurityProfile{}
		}
		update.Properties.SecurityProfile.EncryptionAtHost = utils.Bool(d.Get("encryption_at_host_enabled").(bool))
	}

	if d.HasChange("license_type") {
		shouldUpdate = true

		license := d.Get("license_type").(string)
		if license == "" {
			// Only for create no specification is possible in the API. API does not allow empty string in update.
			// So removing attribute license_type from Terraform configuration if it was set to value other than 'None' would lead to an endless loop in apply.
			// To allow updating in this case set value explicitly to 'None'.
			license = "None"
		}
		update.Properties.LicenseType = &license
	}

	if d.HasChange("user_data") {
		shouldUpdate = true
		update.Properties.UserData = utils.String(d.Get("user_data").(string))
	}

	if instanceView.Model != nil && instanceView.Model.Statuses != nil {
		for _, status := range *instanceView.Model.Statuses {
			if status.Code == nil {
				continue
			}

			// could also be the provisioning state which we're not bothered with here
			state := strings.ToLower(*status.Code)
			if !strings.HasPrefix(state, "powerstate/") {
				continue
			}

			state = strings.TrimPrefix(state, "powerstate/")
			switch strings.ToLower(state) {
			case "deallocated":
				// VM already deallocated, no shutdown and deallocation needed anymore
				shouldShutDown = false
				shouldDeallocate = false
			case "deallocating":
				// VM is deallocating
				// To make sure we do not start updating before this action has finished,
				// only skip the shutdown and send another deallocation request if shouldDeallocate == true
				shouldShutDown = false
			case "stopped":
				// VM already stopped, no shutdown needed anymore
				shouldShutDown = false
			}
		}
	}

	if shouldShutDown {
		log.Printf("[DEBUG] Shutting Down %s..", id)
		forceShutdown := false
		if err = client.PowerOffThenPoll(ctx, *id, virtualmachines.PowerOffOperationOptions{SkipShutdown: pointer.To(forceShutdown)}); err != nil {
			return fmt.Errorf("sending Power Off to %s: %+v", id, err)
		}

		log.Printf("[DEBUG] Shut Down %s..", id)
	}

	if shouldDeallocate {
		if !hasEphemeralOSDisk {
			log.Printf("[DEBUG] Deallocating %s", id)
			// Upgrading to the 2021-07-01 exposed a new hibernate parameter in the Deallocate method
			if err = client.DeallocateThenPoll(ctx, *id, virtualmachines.DeallocateOperationOptions{Hibernate: pointer.To(false)}); err != nil {
				return fmt.Errorf("deallocating %s: %+v", id, err)
			}

			log.Printf("[DEBUG] Deallocated %s", id)
		} else {
			// Code="OperationNotAllowed" Message="Operation 'deallocate' is not supported for VMs or VM Scale Set instances using an ephemeral OS disk."
			log.Printf("[DEBUG] Skipping deallocation for %s since cannot deallocate a Virtual Machine with an Ephemeral OS Disk", id)
		}
	}

	// now the VM's shutdown/deallocated we can update the disk which can't be done via the VM API:
	// Code="ResizeDiskError" Message="Managed disk resize via Virtual Machine [name] is not allowed. Please resize disk resource at [id]."
	// Portal: "Disks can be resized or account type changed only when they are unattached or the owner VM is deallocated."
	if d.HasChange("os_disk.0.disk_size_gb") {
		diskName := d.Get("os_disk.0.name").(string)
		newSize := d.Get("os_disk.0.disk_size_gb").(int)
		log.Printf("[DEBUG] Resizing OS Disk %q for %s to %dGB..", diskName, id, newSize)

		disksClient := meta.(*clients.Client).Compute.DisksClient
		subscriptionId := meta.(*clients.Client).Account.SubscriptionId
		id := disks.NewDiskID(subscriptionId, id.ResourceGroupName, diskName)

		update := disks.DiskUpdate{
			Properties: &disks.DiskUpdateProperties{
				DiskSizeGB: utils.Int64(int64(newSize)),
			},
		}

		err := disksClient.UpdateThenPoll(ctx, id, update)
		if err != nil {
			return fmt.Errorf("resizing OS Disk %q for Windows Virtual Machine %q (Resource Group %q): %+v", diskName, id.DiskName, id.ResourceGroupName, err)
		}

		log.Printf("[DEBUG] Resized OS Disk %q for Windows Virtual Machine %q (Resource Group %q) to %dGB.", diskName, id.DiskName, id.ResourceGroupName, newSize)
	}

	if d.HasChange("os_disk.0.disk_encryption_set_id") {
		if diskEncryptionSetId := d.Get("os_disk.0.disk_encryption_set_id").(string); diskEncryptionSetId != "" {
			diskName := d.Get("os_disk.0.name").(string)
			log.Printf("[DEBUG] Updating encryption settings of OS Disk %q for %s to %q..", diskName, id, diskEncryptionSetId)

			encryptionType, err := retrieveDiskEncryptionSetEncryptionType(ctx, meta.(*clients.Client).Compute.DiskEncryptionSetsClient, diskEncryptionSetId)
			if err != nil {
				return err
			}

			disksClient := meta.(*clients.Client).Compute.DisksClient
			subscriptionId := meta.(*clients.Client).Account.SubscriptionId
			id := disks.NewDiskID(subscriptionId, id.ResourceGroupName, diskName)

			update := disks.DiskUpdate{
				Properties: &disks.DiskUpdateProperties{
					Encryption: &disks.Encryption{
						Type:                encryptionType,
						DiskEncryptionSetId: utils.String(diskEncryptionSetId),
					},
				},
			}

			err = disksClient.UpdateThenPoll(ctx, id, update)
			if err != nil {
				return fmt.Errorf("updating encryption settings of OS Disk %q for Windows Virtual Machine %q (Resource Group %q): %+v", diskName, id.DiskName, id.ResourceGroupName, err)
			}

			log.Printf("[DEBUG] Updating encryption settings of OS Disk %q for Windows Virtual Machine %q (Resource Group %q) to %q.", diskName, id.DiskName, id.ResourceGroupName, diskEncryptionSetId)
		} else {
			return fmt.Errorf("once a customer-managed key is used, you can’t change the selection back to a platform-managed key")
		}
	}

	if shouldUpdate {
		log.Printf("[DEBUG] Updating %s..", id)
		if err := client.UpdateThenPoll(ctx, *id, update); err != nil {
			return fmt.Errorf("updating %s: %+v", id, err)
		}

		log.Printf("[DEBUG] Updated %s", id)
	}

	// if we've shut it down and it was turned off, let's boot it back up
	if shouldTurnBackOn && (shouldShutDown || shouldDeallocate) {
		log.Printf("[DEBUG] Starting %s", id)
		if err = client.StartThenPoll(ctx, *id); err != nil {
			return fmt.Errorf("starting %s: %+v", id, err)
		}

		log.Printf("[DEBUG] Started %s", id)
	}

	return resourceWindowsVirtualMachineRead(d, meta)
}

func resourceWindowsVirtualMachineDelete(d *pluginsdk.ResourceData, meta interface{}) error {
	client := meta.(*clients.Client).Compute.VMClient
	ctx, cancel := timeouts.ForDelete(meta.(*clients.Client).StopContext, d)
	defer cancel()

	id, err := virtualmachines.ParseVirtualMachineID(d.Id())
	if err != nil {
		return err
	}

	locks.ByName(id.VirtualMachineName, VirtualMachineResourceName)
	defer locks.UnlockByName(id.VirtualMachineName, VirtualMachineResourceName)

	log.Printf("[DEBUG] Retrieving %s", id)
	existing, err := client.Get(ctx, *id, virtualmachines.DefaultGetOperationOptions())
	if err != nil {
		if response.WasNotFound(existing.HttpResponse) {
			return nil
		}

		return fmt.Errorf("retrieving %s: %+v", id, err)
	}

	if !meta.(*clients.Client).Features.VirtualMachine.SkipShutdownAndForceDelete {
		// If the VM was in a Failed state we can skip powering off, since that'll fail
		if existing.Model != nil && existing.Model.Properties != nil && existing.Model.Properties.ProvisioningState != nil && strings.EqualFold(*existing.Model.Properties.ProvisioningState, "failed") {
			log.Printf("[DEBUG] Powering Off Windows Virtual Machine was skipped because the VM was in %q state %s", *existing.Model.Properties.ProvisioningState, id)
		} else {
			// ISSUE: 4920
			// shutting down the Virtual Machine prior to removing it means users are no longer charged for some Azure resources
			// thus this can be a large cost-saving when deleting larger instances
			// https://docs.microsoft.com/en-us/azure/virtual-machines/states-lifecycle
			log.Printf("[DEBUG] Powering Off %s", id)
			skipShutdown := !meta.(*clients.Client).Features.VirtualMachine.GracefulShutdown
			if err = client.PowerOffThenPoll(ctx, *id, virtualmachines.PowerOffOperationOptions{SkipShutdown: utils.Bool(skipShutdown)}); err != nil {
				return fmt.Errorf("powering off Windows Virtual Machine %s: %+v", id, err)
			}
			log.Printf("[DEBUG] Powered Off %s", id)
		}
	}

	log.Printf("[DEBUG] Deleting %s.", id)

	// Force Delete is in an opt-in Preview and can only be specified (true/false) if the feature is enabled
	// as such we default this to `nil` which matches the previous behaviour (where this isn't sent) and
	// conditionally set this if required
	var forceDeletion *bool = nil
	if meta.(*clients.Client).Features.VirtualMachine.SkipShutdownAndForceDelete {
		forceDeletion = utils.Bool(true)
	}
	if err = client.DeleteThenPoll(ctx, *id, virtualmachines.DeleteOperationOptions{ForceDeletion: forceDeletion}); err != nil {
		return fmt.Errorf("deleting %s: %+v", id, err)
	}
	log.Printf("[DEBUG] Deleted %s.", id)

	deleteOSDisk := meta.(*clients.Client).Features.VirtualMachine.DeleteOSDiskOnDeletion
	if deleteOSDisk {
		log.Printf("[DEBUG] Deleting OS Disk from %s", id)
		disksClient := meta.(*clients.Client).Compute.DisksClient
		managedDiskId := ""
		if model := existing.Model; model != nil {
			if props := existing.Model.Properties; props != nil && props.StorageProfile != nil && props.StorageProfile.OsDisk != nil {
				if disk := props.StorageProfile.OsDisk.ManagedDisk; disk != nil && disk.Id != nil {
					managedDiskId = *disk.Id
				}
			}
		}

		if managedDiskId != "" {
			diskId, err := disks.ParseDiskID(managedDiskId)
			if err != nil {
				return err
			}

			diskDeleteFuture, err := disksClient.Delete(ctx, *diskId)
			if err != nil {
				if !response.WasNotFound(diskDeleteFuture.HttpResponse) {
					return fmt.Errorf("deleting OS Disk %q (Resource Group %q) for %s: %+v", diskId.DiskName, diskId.ResourceGroupName, err)
				}
			}
			if !response.WasNotFound(diskDeleteFuture.HttpResponse) {
				if err := diskDeleteFuture.Poller.PollUntilDone(ctx); err != nil {
					return fmt.Errorf("OS Disk %q (Resource Group %q) for %s: %+v", diskId.DiskName, diskId.ResourceGroupName, id, err)
				}
			}

			log.Printf("[DEBUG] Deleted OS Disk from Windows Virtual Machine %q (Resource Group %q).", diskId.DiskName, diskId.ResourceGroupName)
		} else {
			log.Printf("[DEBUG] Skipping Deleting OS Disk from %s - cannot determine OS Disk ID.", id)
		}
	} else {
		log.Printf("[DEBUG] Skipping Deleting OS Disk from %s", id)
	}

	// Need to add a get and a state wait to avoid bug in network API where the attached disk(s) are not actually deleted
	// Service team indicated that we need to do a get after VM delete call returns to verify that the VM and all attached
	// disks have actually been deleted.

	log.Printf("[INFO] verifying %s has been deleted", id)
	virtualMachine, err := client.Get(ctx, *id, virtualmachines.DefaultGetOperationOptions())
	if err != nil && !response.WasNotFound(virtualMachine.HttpResponse) {
		return fmt.Errorf("verifying %shas been deleted: %+v", id, err)
	}

	if !response.WasNotFound(virtualMachine.HttpResponse) {
		log.Printf("[INFO] Windows Virtual Machine still exists, waiting on %s to be deleted", id)

		deleteWait := &pluginsdk.StateChangeConf{
			Pending:    []string{"200"},
			Target:     []string{"404"},
			MinTimeout: 30 * time.Second,
			Timeout:    d.Timeout(pluginsdk.TimeoutDelete),
			Refresh: func() (interface{}, string, error) {
				log.Printf("[INFO] checking on state of %s", id)
				resp, err := client.Get(ctx, *id, virtualmachines.DefaultGetOperationOptions())
				if err != nil {
					if response.WasNotFound(resp.HttpResponse) {
						return resp, strconv.Itoa(resp.HttpResponse.StatusCode), nil
					}
					return nil, "nil", fmt.Errorf("polling for the status of %s %v", id, err)
				}
				return resp, strconv.Itoa(resp.HttpResponse.StatusCode), nil
			},
		}

		if _, err := deleteWait.WaitForStateContext(ctx); err != nil {
			return fmt.Errorf("waiting for the deletion of %s: %v", id, err)
		}
	}

	return nil
}
