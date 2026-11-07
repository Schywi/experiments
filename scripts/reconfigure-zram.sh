#!/usr/bin/env bash
set -euo pipefail

# 1. Turn off swap on zram0
swapoff /dev/zram0 2>/dev/null || true

# 2. Reset the device so comp_algorithm is not locked by the kernel
if [ -e /sys/block/zram0/reset ]; then
  echo 1 > /sys/block/zram0/reset
fi

# 3. Write clean config
cat << 'EOF' > /etc/systemd/zram-generator.conf
[zram0]
zram-size = 16384
compression-algorithm = zstd
EOF

# 4. Reload and restart systemd service
systemctl daemon-reload
systemctl restart systemd-zram-setup@zram0.service

# 5. Show resulting zram status
zramctl
