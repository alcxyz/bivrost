variable "tenant_id" {
  description = "Existing Microsoft Entra tenant ID."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", var.tenant_id))
    error_message = "tenant_id must be a GUID."
  }
}

variable "subscription_id" {
  description = "Subscription containing the existing resource group."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", var.subscription_id))
    error_message = "subscription_id must be a GUID."
  }
}

variable "location" {
  description = "Azure Public region for the new account and private endpoint; must match the existing subnet VNet region."
  type        = string
}

variable "resource_group_name" {
  description = "Existing resource group name; this example does not own the group."
  type        = string
}

variable "storage_account_name" {
  description = "Globally unique name for the new, dedicated StorageV2 account."
  type        = string

  validation {
    condition     = can(regex("^[a-z0-9]{3,24}$", var.storage_account_name))
    error_message = "Use 3–24 lowercase letters or digits for the storage account name."
  }
}

variable "container_name" {
  description = "Private metadata container name."
  type        = string
  default     = "heimdal"

  validation {
    condition     = can(regex("^[a-z0-9](?:[a-z0-9-]{1,61})[a-z0-9]$", var.container_name)) && !can(regex("--", var.container_name))
    error_message = "Use 3–63 lowercase letters, digits, or single hyphens; start and end with a letter or digit."
  }
}

variable "private_endpoint_subnet_id" {
  description = "ID of the existing subnet for the Blob private endpoint."
  type        = string

  validation {
    condition     = can(regex("(?i)^/subscriptions/[0-9a-fA-F-]{36}/resourceGroups/[^/]+/providers/Microsoft\\.Network/virtualNetworks/[^/]+/subnets/[^/]+$", var.private_endpoint_subnet_id))
    error_message = "private_endpoint_subnet_id must be a full Azure subnet resource ID."
  }
}

variable "blob_private_dns_zone_id" {
  description = "ID of an existing privatelink.blob.core.windows.net private DNS zone. Its resolution path for publishers and consumers must already exist."
  type        = string

  validation {
    condition     = can(regex("(?i)^/subscriptions/[0-9a-fA-F-]{36}/resourceGroups/[^/]+/providers/Microsoft\\.Network/privateDnsZones/privatelink\\.blob\\.core\\.windows\\.net$", var.blob_private_dns_zone_id))
    error_message = "blob_private_dns_zone_id must identify a privatelink.blob.core.windows.net zone."
  }
}

variable "consumer_group_object_ids" {
  description = "Existing Entra group object IDs granted container-scoped Storage Blob Data Reader."
  type        = set(string)

  validation {
    condition     = alltrue([for id in var.consumer_group_object_ids : can(regex("^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", id))])
    error_message = "Every consumer group object ID must be a GUID."
  }
}

variable "publisher_group_object_ids" {
  description = "Existing Entra group object IDs granted container-scoped Storage Blob Data Contributor. Human activation policy is managed separately."
  type        = set(string)

  validation {
    condition     = alltrue([for id in var.publisher_group_object_ids : can(regex("^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", id))])
    error_message = "Every publisher group object ID must be a GUID."
  }
}

variable "ci_principal_object_ids" {
  description = "Existing CI service principal object IDs granted container-scoped Storage Blob Data Contributor. Federation is managed separately."
  type        = set(string)

  validation {
    condition     = alltrue([for id in var.ci_principal_object_ids : can(regex("^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$", id))])
    error_message = "Every CI principal object ID must be a GUID."
  }
}
