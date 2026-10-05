variable "domain" {
  type    = string
  default = "runlink.dev"
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]*\\.[a-z]{2,}$", var.domain))
    error_message = "Use a DNS hostname with lowercase letters."
  }
}
variable "turn_domain" {
  type    = string
  default = "turn.runlink.dev"
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9.-]*\\.[a-z]{2,}$", var.turn_domain))
    error_message = "Use a DNS hostname with lowercase letters."
  }
}
variable "acme_email" {
  type = string
  validation {
    condition     = can(regex("^[a-zA-Z0-9._+-]+@[a-zA-Z0-9.-]+$", var.acme_email))
    error_message = "Provide the operator's ACME email."
  }
}
variable "location" {
  type    = string
  default = "fsn1"
  validation {
    condition     = contains(["fsn1", "nbg1", "hel1"], var.location)
    error_message = "The v1 stack uses one eu-central location."
  }
}
variable "server_type" {
  type    = string
  default = "cx23"
  validation {
    condition     = contains(["cx23", "cx33", "cx43"], var.server_type)
    error_message = "Choose a supported x86_64 CX server type."
  }
}
variable "ssh_public_key" {
  type = string
  validation {
    condition     = can(regex("^ssh-ed25519 [A-Za-z0-9+/=]+(?: [^\\r\\n]*)?$", var.ssh_public_key))
    error_message = "Supply a single Ed25519 administrator public key."
  }
}
variable "release_public_key" {
  type = string
  validation {
    condition     = can(regex("^ssh-ed25519 [A-Za-z0-9+/=]+(?: [^\\r\\n]*)?$", var.release_public_key))
    error_message = "Supply a separate release operator's Ed25519 SSH public key."
  }
}
variable "admin_cidrs" {
  type = list(string)
  validation {
    condition     = length(var.admin_cidrs) > 0 && alltrue([for cidr in var.admin_cidrs : can(cidrhost(cidr, 0)) && try(tonumber(split("/", cidr)[1]) > 0, false)])
    error_message = "Restrict SSH to administrator IPv4/IPv6 CIDRs."
  }
}
