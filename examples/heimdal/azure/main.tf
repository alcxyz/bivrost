locals {
  existing_resource_group_id = "/subscriptions/${var.subscription_id}/resourceGroups/${var.resource_group_name}"
}

# ARM resources avoid Shared Key and storage data-plane access during bootstrap.
# Keep this account dedicated to Heimdal; no state backend resources are created.
resource "azapi_resource" "metadata_account" {
  type      = "Microsoft.Storage/storageAccounts@2024-01-01"
  name      = var.storage_account_name
  parent_id = local.existing_resource_group_id
  location  = var.location

  # Removal/replacement requires an explicit reviewed change to this guard.
  # This does not prevent out-of-band Azure deletion or removal of this block.
  lifecycle {
    prevent_destroy = true
  }

  body = {
    kind = "StorageV2"
    sku = {
      name = "Standard_LRS"
    }
    properties = {
      allowBlobPublicAccess        = false
      allowSharedKeyAccess         = false
      defaultToOAuthAuthentication = true
      minimumTlsVersion            = "TLS1_2"
      publicNetworkAccess          = "Disabled"
      supportsHttpsTrafficOnly     = true
    }
  }
}

resource "azapi_update_resource" "blob_service" {
  type        = "Microsoft.Storage/storageAccounts/blobServices@2024-01-01"
  resource_id = "${azapi_resource.metadata_account.id}/blobServices/default"

  body = {
    properties = {
      isVersioningEnabled = true
      deleteRetentionPolicy = {
        enabled = true
        days    = 14
      }
      containerDeleteRetentionPolicy = {
        enabled = true
        days    = 14
      }
    }
  }
}

resource "azapi_resource" "metadata_container" {
  type      = "Microsoft.Storage/storageAccounts/blobServices/containers@2024-01-01"
  name      = var.container_name
  parent_id = azapi_update_resource.blob_service.resource_id

  body = {
    properties = {
      publicAccess = "None"
    }
  }
}

resource "azurerm_private_endpoint" "blob" {
  name                = "${var.storage_account_name}-blob-pe"
  location            = var.location
  resource_group_name = var.resource_group_name
  subnet_id           = var.private_endpoint_subnet_id

  lifecycle {
    precondition {
      condition     = lower(split("/", var.private_endpoint_subnet_id)[2]) == lower(var.subscription_id) && lower(split("/", var.blob_private_dns_zone_id)[2]) == lower(var.subscription_id)
      error_message = "The existing subnet and Blob private DNS zone must be in subscription_id."
    }
  }

  private_service_connection {
    name                           = "${var.storage_account_name}-blob"
    private_connection_resource_id = azapi_resource.metadata_account.id
    subresource_names              = ["blob"]
    is_manual_connection           = false
  }

  private_dns_zone_group {
    name                 = "blob"
    private_dns_zone_ids = [var.blob_private_dns_zone_id]
  }
}

resource "azurerm_role_assignment" "consumer" {
  for_each             = var.consumer_group_object_ids
  scope                = azapi_resource.metadata_container.id
  role_definition_name = "Storage Blob Data Reader"
  principal_id         = each.value
  principal_type       = "Group"
}

resource "azurerm_role_assignment" "publisher_group" {
  for_each             = var.publisher_group_object_ids
  scope                = azapi_resource.metadata_container.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = each.value
  principal_type       = "Group"
}

resource "azurerm_role_assignment" "ci_publisher" {
  for_each             = var.ci_principal_object_ids
  scope                = azapi_resource.metadata_container.id
  role_definition_name = "Storage Blob Data Contributor"
  principal_id         = each.value
  principal_type       = "ServicePrincipal"
}
