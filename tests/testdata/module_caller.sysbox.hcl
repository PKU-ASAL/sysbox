module "net" {
  source        = "./modules/three-tier-net"
  cidr_dmz      = "10.1.1.0/24"
  cidr_internal = "10.1.2.0/24"
}
