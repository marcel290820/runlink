# This provider is mocked for every run: no cloud token or API is used.
mock_provider "hcloud" {
  mock_resource "hcloud_network" { defaults = { id = "100" } }
  mock_resource "hcloud_network_subnet" { defaults = { id = "101" } }
  mock_resource "hcloud_ssh_key" { defaults = { id = "102" } }
  mock_resource "hcloud_firewall" { defaults = { id = "103" } }
  mock_resource "hcloud_primary_ip" { defaults = { id = "104" } }
}
variables {
  acme_email         = "operator@example.invalid"
  ssh_public_key     = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJSpaKlGL6SXvqIzLnkbBa8QqMJDmwrSsYgZZykmgAQw fixture-admin"
  release_public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJSpaKlGL6SXvqIzLnkbBa8QqMJDmwrSsYgZZykmgAQw fixture-release"
  admin_cidrs        = ["203.0.113.10/32", "2001:db8:ffff::1/128"]
}
override_resource {
  target = hcloud_primary_ip.ipv4["a"]
  values = { ip_address = "198.51.100.10" }
}
override_resource {
  target = hcloud_primary_ip.ipv4["b"]
  values = { ip_address = "198.51.100.20" }
}
override_resource {
  target = hcloud_primary_ip.ipv4["c"]
  values = { ip_address = "198.51.100.30" }
}
override_resource {
  target = hcloud_primary_ip.ipv6["a"]
  values = { ip_network = "2001:db8:a::/64" }
}
override_resource {
  target = hcloud_primary_ip.ipv6["b"]
  values = { ip_network = "2001:db8:b::/64" }
}
override_resource {
  target = hcloud_primary_ip.ipv6["c"]
  values = { ip_network = "2001:db8:c::/64" }
}
run "three_server_stack" {
  command = apply
  assert {
    condition     = length(hcloud_server.runlink) == 3
    error_message = "Exactly three role-separated servers are required."
  }
  assert {
    condition     = length(local.ports.b) == 0 && length(local.ports.c) == 5
    error_message = "B must have no public application port; C needs TCP/TLS and bounded UDP relay."
  }
  assert {
    condition     = alltrue([for server in hcloud_server.runlink : server.delete_protection && server.rebuild_protection])
    error_message = "Protect the new stack against accidental deletion/rebuild."
  }
  assert {
    condition     = strcontains(hcloud_server.runlink["c"].user_data, "meta skuid") && length([for file in local.cloud_config.c.write_files : file if file.path == "/etc/runlink/turn-secret"]) == 0
    error_message = "Enforce the TURN UID boundary and keep shared secrets out of cloud state."
  }
}
run "reject_public_ssh" {
  command = plan
  variables { admin_cidrs = ["0.0.0.0/0"] }
  expect_failures = [var.admin_cidrs]
}

run "reject_equivalent_public_ssh" {
  command = plan
  variables { admin_cidrs = ["203.0.113.10/0"] }
  expect_failures = [var.admin_cidrs]
}
