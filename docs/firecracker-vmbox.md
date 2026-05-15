ᕕ( ᐛ )ᕗ Jimyag's Blog
Home Friends RSS Blog ☾ Search
从 0 启动 firecracker 虚拟机
2025-11-10 · 5476 words · ~ 26 min read

下载 firecracker 和 kernel, rootfs
mkdir -p ~/opt/firecracker && cd ~/opt/firecracker

wget https://github.com/firecracker-microvm/firecracker/releases/download/v1.14.0/firecracker-v1.14.0-x86_64.tgz

tar -xzf firecracker-v1.14.0-x86_64.tgz

wget "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64/vmlinux-5.10.245" -O vmlinux

wget "https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.14/x86_64/ubuntu-24.04.squashfs" -O ubuntu.squashfs
"Copy"
squashfs 转 ext4
sudo apt-get install squashfs-tools

sudo unsquashfs -d /tmp/rootfs ubuntu.squashfs

dd if=/dev/zero of=rootfs.ext4 bs=1M count=1024
mkfs.ext4 rootfs.ext4

sudo mkdir -p /mnt/rootfs
sudo mount rootfs.ext4 /mnt/rootfs
sudo cp -a /tmp/rootfs/* /mnt/rootfs/
sudo umount /mnt/rootfs

sudo rm -rf /tmp/rootfs
"Copy"
配置宿主机网络
sudo ip tuntap add ftap0 mode tap
sudo ip addr add 172.16.0.1/24 dev ftap0
sudo ip link set ftap0 up

sudo sysctl -w net.ipv4.ip_forward=1

# 配置 NAT 将虚拟机流量转发到宿主机
# eth0 替换为你的出口网卡名
IFNAME=eth0
sudo iptables -t nat -A POSTROUTING -o $IFNAME -j MASQUERADE
sudo iptables -A FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
sudo iptables -A FORWARD -i ftap0 -o $IFNAME -j ACCEPT
"Copy"
使用纯命令行启动
启动 firecracker
单独开一个终端执行

cd ~/opt/firecracker/release-v1.14.0-x86_64
sudo rm -f /tmp/firecracker.socket
sudo ./firecracker-v1.14.0-x86_64 --api-sock /tmp/firecracker.socket
"Copy"
配置 firecracker
在另一个终端执行（工作目录为 ~/opt/firecracker）：

cd ~/opt/firecracker

# 1. 配置 kernel
sudo curl --unix-socket /tmp/firecracker.socket -i \
    -X PUT 'http://localhost/boot-source' \
    -H 'Accept: application/json' \
    -H 'Content-Type: application/json' \
    -d "{
        \"kernel_image_path\": \"$(pwd)/vmlinux\",
        \"boot_args\": \"console=ttyS0 reboot=k panic=1 pci=off\"
    }"

# 2. 配置 rootfs
sudo curl --unix-socket /tmp/firecracker.socket -i \
    -X PUT 'http://localhost/drives/rootfs' \
    -H 'Accept: application/json' \
    -H 'Content-Type: application/json' \
    -d "{
        \"drive_id\": \"rootfs\",
        \"path_on_host\": \"$(pwd)/rootfs.ext4\",
        \"is_root_device\": true,
        \"is_read_only\": false
    }"

# 3. 配置网络
sudo curl --unix-socket /tmp/firecracker.socket -i \
    -X PUT 'http://localhost/network-interfaces/eth0' \
    -H 'Accept: application/json' \
    -H 'Content-Type: application/json' \
    -d "{
        \"iface_id\": \"eth0\",
        \"guest_mac\": \"AA:FC:00:00:00:01\",
        \"host_dev_name\": \"ftap0\"
    }"

# 4. 配置 CPU/内存
sudo curl --unix-socket /tmp/firecracker.socket -i \
    -X PUT 'http://localhost/machine-config' \
    -H 'Accept: application/json' \
    -H 'Content-Type: application/json' \
    -d "{
        \"vcpu_count\": 2,
        \"mem_size_mib\": 1024
    }"

# 5. 启动 VM
sudo curl --unix-socket /tmp/firecracker.socket -i \
    -X PUT 'http://localhost/actions' \
    -H 'Accept: application/json' \
    -H 'Content-Type: application/json' \
    -d "{
        \"action_type\": \"InstanceStart\"
    }"
"Copy"
即可看到下面的日志，说明虚拟机启动成功

点击展开完整启动日志
Guest 内网络配置
在 Guest 内执行

# 配置 IP
ip addr add 172.16.0.2/24 dev eth0

# 配置网关
ip route add default via 172.16.0.1

# 配置 DNS
echo "nameserver 8.8.8.8" > /etc/resolv.conf

# 验证
ping baidu.com
"Copy"
清理
# 在 vm 内执行
sudo poweroff

# 在宿主机上删除 tap 设备
sudo ip link del ftap0

# 清理 iptables 规则
# 在宿主机上清理 iptables 规则
IFNAME=eth0
sudo iptables -t nat -D POSTROUTING -o $IFNAME -j MASQUERADE
sudo iptables -D FORWARD -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT
sudo iptables -D FORWARD -i ftap0 -o $IFNAME -j ACCEPT
"Copy"
使用配置文件启动
mkdir -p ~/opt/firecracker/vm && cat > ~/opt/firecracker/vm/config.json << 'EOF'
{
  "boot-source": {
      "kernel_image_path": "~/opt/firecracker/vmlinux",
      "boot_args": "console=ttyS0 reboot=t panic=1 pci=off"
    },
    "drives": [{
      "drive_id": "rootfs",
      "path_on_host": "~/opt/firecracker/rootfs.ext4",
      "is_root_device": true,
      "is_read_only": false
    }],
    "network-interfaces": [{
      "iface_id": "eth0",
      "host_dev_name": "ftap0",
      "guest_mac": "AA:FC:00:00:00:01"
    }],
    "machine-config": {
      "vcpu_count": 2,
      "mem_size_mib": 1024
    }
}
EOF

sudo ~/opt/firecracker/release-v1.14.0-x86_64/firecracker-v1.14.0-x86_64 --id vm-1 --boot-timer --no-api --config-file ~/opt/firecracker/vm/config.json
"Copy"
#Firecracker

← Previous
Go Context 源码阅读
Next →
cursor、vscode 插件以及配置
Table of Contents
下载 firecracker 和 kernel, rootfs
squashfs 转 ext4
配置宿主机网络
使用纯命令行启动
启动 firecracker
配置 firecracker
Guest 内网络配置
清理
使用配置文件启动
