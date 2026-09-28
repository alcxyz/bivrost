# Informational source locator only; this is not a Bivrost onboarding schema.
output "source_locator" {
  description = "Credential-free coordinates for an administrator to turn into a trusted bootstrap reference."
  value = {
    tenant_id       = var.tenant_id
    subscription_id = var.subscription_id
    account_name    = var.storage_account_name
    container_name  = var.container_name
    blob_endpoint   = "https://${var.storage_account_name}.blob.core.windows.net"
  }
}

output "owned_resource_ids" {
  description = "Resources owned by this example's state; excludes the referenced resource group, subnet, DNS zone, identities, and their policies."
  value = {
    storage_account  = azapi_resource.metadata_account.id
    container        = azapi_resource.metadata_container.id
    private_endpoint = azurerm_private_endpoint.blob.id
    consumer_grants  = [for grant in azurerm_role_assignment.consumer : grant.id]
    publisher_grants = [for grant in azurerm_role_assignment.publisher_group : grant.id]
    ci_grants        = [for grant in azurerm_role_assignment.ci_publisher : grant.id]
  }
}
