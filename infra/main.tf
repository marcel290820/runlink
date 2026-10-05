locals {
  roles       = toset(["a", "b", "c"])
  private_ips = { a = "10.77.0.10", b = "10.77.0.20", c = "10.77.0.30" }
  public4     = { for role in local.roles : role => hcloud_primary_ip.ipv4[role].ip_address }
  public6     = { for role in local.roles : role => cidrhost(hcloud_primary_ip.ipv6[role].ip_network, 1) }
  ports = {
    a = [{ protocol = "tcp", port = "80" }, { protocol = "tcp", port = "443" }]
    b = []
    c = [{ protocol = "tcp", port = "80" }, { protocol = "udp", port = "3478" }, { protocol = "tcp", port = "3478" }, { protocol = "tcp", port = "5349" }, { protocol = "udp", port = "49160-49259" }]
  }
  template_vars = {
    for role in local.roles : role => {
      role        = role
      private_ip  = local.private_ips[role]
      frontend_ip = local.private_ips.a
      app_ip      = local.private_ips.b
      domain      = var.domain
      turn_domain = var.turn_domain
      tls_domain  = role == "a" ? var.domain : var.turn_domain
      tls_unit    = role == "a" ? "runlink-frontend" : "runlink-turn"
      acme_email  = var.acme_email
      admin4      = join(", ", [for cidr in var.admin_cidrs : cidr if !strcontains(cidr, ":")])
      admin6      = join(", ", [for cidr in var.admin_cidrs : cidr if strcontains(cidr, ":")])
      frontend4   = local.public4.a
      app4        = local.public4.b
      turn4       = local.public4.c
      frontend6   = local.public6.a
      app6        = local.public6.b
      turn6       = local.public6.c
    }
  }
  cloud_config = {
    for role in local.roles : role => {
      package_update  = true
      package_upgrade = true
      packages        = concat(["ca-certificates", "python3", "nftables", "unattended-upgrades"], role != "b" ? ["certbot"] : [], [for package in ["coturn"] : [package, "4.6.1-1build4"] if role == "c"])
      users = [
        "default",
        { name = "runlink", uid = 980, system = true, shell = "/usr/sbin/nologin", lock_passwd = true },
        { name = "runlink-release", shell = "/bin/bash", lock_passwd = true, ssh_authorized_keys = [var.release_public_key] }
      ]
      write_files = concat([
        { path = "/etc/nftables.conf", permissions = "0600", content = templatefile("${path.module}/templates/firewall.nft.tftpl", local.template_vars[role]) },
        { path = "/usr/local/lib/runlink/initialize.sh", permissions = "0755", content = templatefile("${path.module}/templates/initialize.sh.tftpl", local.template_vars[role]) }
        ], role != "c" ? [
        { path = "/etc/runlink/server.json", permissions = "0644", content = jsonencode(role == "a" ? { role = "frontend", listen = "[::]:443", upstream = "http://${local.private_ips.b}:8081", tls_cert = "/etc/runlink/tls/fullchain.pem", tls_key = "/etc/runlink/tls/privkey.pem" } : { role = "app", listen = "${local.private_ips.b}:8081", state_dir = "/var/lib/runlink" }) },
        { path = "/etc/systemd/system/runlink-${role == "a" ? "frontend" : "app"}.service", permissions = "0644", content = templatefile("${path.module}/templates/runlink.service.tftpl", local.template_vars[role]) }
        ] : [
        { path = "/etc/runlink/turnserver.base.conf", permissions = "0644", content = templatefile("${path.module}/templates/turnserver.conf.tftpl", local.template_vars[role]) },
        { path = "/usr/local/lib/runlink/configure-turn.py", permissions = "0755", content = file("${path.module}/templates/configure-turn.py") },
        { path = "/etc/systemd/system/runlink-turn.service", permissions = "0644", content = file("${path.module}/templates/runlink-turn.service") },
        ], role != "b" ? [
        { path = "/etc/letsencrypt/renewal-hooks/deploy/runlink-tls", permissions = "0755", content = templatefile("${path.module}/templates/renew-tls.sh.tftpl", local.template_vars[role]) }
      ] : [])
      runcmd = [["bash", "/usr/local/lib/runlink/initialize.sh"]]
    }
  }
}
resource "hcloud_ssh_key" "admin" {
  name       = "runlink-admin"
  public_key = var.ssh_public_key
}
resource "hcloud_network" "runlink" {
  name     = "runlink-v1"
  ip_range = "10.77.0.0/16"
}
resource "hcloud_network_subnet" "runlink" {
  network_id   = hcloud_network.runlink.id
  type         = "cloud"
  network_zone = "eu-central"
  ip_range     = "10.77.0.0/24"
}
resource "hcloud_primary_ip" "ipv4" {
  for_each          = local.roles
  name              = "runlink-${each.key}-v4"
  location          = var.location
  type              = "ipv4"
  auto_delete       = false
  delete_protection = true
}
resource "hcloud_primary_ip" "ipv6" {
  for_each          = local.roles
  name              = "runlink-${each.key}-v6"
  location          = var.location
  type              = "ipv6"
  auto_delete       = false
  delete_protection = true
}
resource "hcloud_firewall" "runlink" {
  for_each = local.roles
  name     = "runlink-${each.key}"
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "22"
    source_ips = var.admin_cidrs
  }
  rule {
    direction  = "in"
    protocol   = "icmp"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  dynamic "rule" {
    for_each = local.ports[each.key]
    content {
      direction  = "in"
      protocol   = rule.value.protocol
      port       = rule.value.port
      source_ips = ["0.0.0.0/0", "::/0"]
    }
  }
}
resource "hcloud_server" "runlink" {
  for_each           = local.roles
  name               = "runlink-${each.key}"
  image              = "ubuntu-24.04"
  server_type        = var.server_type
  location           = var.location
  ssh_keys           = [hcloud_ssh_key.admin.id]
  firewall_ids       = [hcloud_firewall.runlink[each.key].id]
  delete_protection  = true
  rebuild_protection = true
  public_net {
    ipv4_enabled = true
    ipv4         = hcloud_primary_ip.ipv4[each.key].id
    ipv6_enabled = true
    ipv6         = hcloud_primary_ip.ipv6[each.key].id
  }
  network {
    subnet_id = hcloud_network_subnet.runlink.id
    ip        = local.private_ips[each.key]
  }
  user_data = "#cloud-config\n${jsonencode(local.cloud_config[each.key])}\n"
}
output "addresses" {
  value = { for role in local.roles : role => { ipv4 = local.public4[role], ipv6 = local.public6[role], private = local.private_ips[role] } }
}
