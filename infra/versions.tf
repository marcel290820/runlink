terraform {
  required_version = "= 1.13.1"
  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "= 1.69.0"
    }
  }
}
# HCLOUD_TOKEN is supplied by an operator only when provisioning is authorized.
provider "hcloud" {}
