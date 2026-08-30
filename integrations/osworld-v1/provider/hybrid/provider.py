"""osw-hybrid provider —— 让 DesktopEnv 直接驱动 osw-hybrid 容器（容器原生桌面，非 QEMU）。

与官方 docker provider 的关系：结构完全照搬，语义保持一致（同样没有快照，
revert 即销毁重建），但去掉了 QEMU 那一层——桌面直接跑在容器里。

这么做的收益：
  - 每 env 约 1-2G 内存而非 4G，单机并发上限从 ~24 提到 100+
  - MCP 是 stdio 协议，必须与桌面同机；容器原生桌面下 `docker exec -i` 就是
    天然的传输层，不需要往 guest 里塞端口转发或 socat 桥接
  - 迭代快：改一行 runtime.py 立刻能测，不用重启虚拟机

代价：镜像是自建的，分数与官方 leaderboard 不直接可比。这一点必须在报告里写明。
"""

import logging
import ipaddress
import os
import platform
import subprocess
import time

import docker
import psutil
import requests
from filelock import FileLock
from pathlib import Path

from desktop_env.providers.base import Provider

logger = logging.getLogger("desktopenv.providers.hybrid.HybridProvider")
logger.setLevel(logging.INFO)

WAIT_TIME = 2
RETRY_INTERVAL = 1
LOCK_TIMEOUT = 30

# 容器内固定端口，与 ocu-osworld 镜像的 EXPOSE 对应
GUEST_SERVER_PORT = 5000    # OSWorld guest server (flask)
GUEST_CHROMIUM_PORT = 9222  # Chrome CDP —— 由任务 setup 自带的 socat 转发到此端口
GUEST_VNC_PORT = 6080       # noVNC（官方 docker provider 用 8006，这里是 noVNC 的 6080）
GUEST_VLC_PORT = 8080       # VLC HTTP 接口，vlc_playing_info getter 需要

DEFAULT_IMAGE = os.environ.get("OSW_HYBRID_IMAGE", "osw-hybrid:v0.14")

# slot 模式的端口基址。每个并发槽位占用 base+slot，槽位数上限 999。
# 刻意避开宿主已用的 5000-5010 / 6080-6090 / 8080-8090 / 9222-9230 区间。
SLOT_BASE_SERVER = 15000
SLOT_BASE_CHROMIUM = 19000
SLOT_BASE_VNC = 16000
SLOT_BASE_VLC = 18000

# 宿主用 systemd-resolved(127.0.0.53)，Docker 认出这是 loopback 容器不可用，
# 于是回退到 8.8.8.8 —— 而本机出不去公网 DNS，容器会完全无法解析域名，
# 症状是 apt/curl/Chrome 全部超时。必须显式指定 Azure 的内部 DNS。
DEFAULT_DNS = os.environ.get("OSW_HYBRID_DNS", "168.63.129.16")

# 专用网桥。默认 docker0 的 DOCKER-FORWARD 放行规则在本机被人为移除（容器默认
# 不能出网，只有指定网桥放行——同机的 design-harness-bench 也是这么做的），
# 结果是 62 道需要访问真实网站的任务里 agent 只能看到 Chrome 的离线恐龙页，
# 而这与"模型能力"或"MCP 设计"毫无关系，纯属环境限制污染分数。
# 建专用网络后 docker 会自动补齐 MASQUERADE 与 FORWARD 规则，无需手工改防火墙。
DEFAULT_NETWORK = os.environ.get("OSW_HYBRID_NETWORK", "osw-net")
DOCKER_API_TIMEOUT = int(
    os.environ.get("OSW_HYBRID_DOCKER_API_TIMEOUT", "300")
)


class PortAllocationError(Exception):
    pass


class HybridProvider(Provider):
    def __init__(self, region: str = None):
        # Cold-starting many desktop containers can keep Docker API calls
        # pending beyond the SDK's 60-second default even while the daemon is
        # healthy. Treat that as orchestration backpressure, not task failure.
        self.client = docker.from_env(timeout=DOCKER_API_TIMEOUT)
        self.server_port = None
        self.vnc_port = None
        self.chromium_port = None
        self.vlc_port = None
        self.container = None
        self.container_ip = None

        temp_dir = Path(os.getenv("TEMP") if platform.system() == "Windows" else "/tmp")
        self.lock_file = temp_dir / "ocu_port_allocation.lck"
        self.lock_file.parent.mkdir(parents=True, exist_ok=True)

    def _get_used_ports(self):
        """系统已占用 + Docker 已映射的端口。并发起容器时靠这个避免撞端口。"""
        system_ports = set(conn.laddr.port for conn in psutil.net_connections())
        docker_ports = set()
        for container in self.client.containers.list():
            ports = container.attrs["NetworkSettings"]["Ports"]
            if ports:
                for port_mappings in ports.values():
                    if port_mappings:
                        docker_ports.update(int(p["HostPort"]) for p in port_mappings)
        return system_ports | docker_ports

    def _get_available_port(self, start_port: int) -> int:
        used_ports = self._get_used_ports()
        port = start_port
        while port < 65354:
            if port not in used_ports:
                return port
            port += 1
        raise PortAllocationError(f"No available ports found starting from {start_port}")

    def _wait_for_vm_ready(self, timeout: int = 300):
        """就绪 = guest server 能取观测。

        不检查 Chrome：浏览器由任务自己的 setup 步骤启动（config 里带着
        `launch google-chrome --remote-debugging-port=1337` 和 socat 转发），
        容器就绪的时刻它本就不该存在。
        """
        start_time = time.time()
        last = "not probed"

        def screenshot_ok():
            try:
                r = requests.get(
                    f"http://{self.container_ip}:{self.server_port}/screenshot", timeout=(10, 10)
                )
                return r.status_code == 200
            except Exception:
                return False

        while time.time() - start_time < timeout:
            if screenshot_ok():
                logger.info("osw-hybrid container ready (guest server)")
                return True
            last = "guest server /screenshot not answering"
            time.sleep(RETRY_INTERVAL)

        tail = ""
        if self.container:
            try:
                tail = self.container.logs(tail=40).decode("utf-8", "replace")
            except Exception:
                pass
        raise TimeoutError(
            f"osw-hybrid container failed to become ready within {timeout}s "
            f"(last state: {last}).\n--- container logs ---\n{tail}"
        )

    def start_emulator(self, path_to_vm: str, headless: bool = True, os_type: str = "Ubuntu",
                       *args, **kwargs):
        """path_to_vm 在这里被当作镜像名使用（如 osw-hybrid:v0.14）。

        不做端口映射：直接用容器 IP + 容器内原始端口。这样端口永远不冲突，
        并发数不再受宿主端口空间或分配协调的限制（先后踩过全局 filelock 超时
        和 slot 预分配两版方案，都是在解决一个本不该存在的问题）。
        另有一个硬性理由：Chrome 只绑 127.0.0.1:9222，docker 端口映射转发到
        容器的 0.0.0.0:9222 无人监听，表现为 connection reset；而经容器内
        转发器暴露到 9223 后，Chrome 会按 Host 头生成正确的
        webSocketDebuggerUrl，playwright 才能连上。
        """
        image = path_to_vm or DEFAULT_IMAGE
        self.server_port = GUEST_SERVER_PORT
        self.chromium_port = GUEST_CHROMIUM_PORT
        self.vnc_port = GUEST_VNC_PORT
        self.vlc_port = GUEST_VLC_PORT
        try:
            self._run_container(image)
            self.container.reload()
            nets = self.container.attrs["NetworkSettings"]["Networks"]
            self.container_ip = next(iter(nets.values()))["IPAddress"]
            if not self.container_ip:
                raise RuntimeError("container has no IP address")
            logger.info(
                f"Started osw-hybrid container {self.container.short_id} from {image} "
                f"at {self.container_ip} (server:{self.server_port} cdp:{self.chromium_port})"
            )
            self._wait_for_vm_ready()
        except Exception:
            if self.container:
                try:
                    self.container.stop()
                    self.container.remove(v=True)
                except Exception:
                    pass
                self.container = None
            raise

    def _run_container(self, image):
        self._ensure_network_forwarding()
        gateway = self._network_gateway()
        self.container = self.client.containers.run(
            image,
            command=["sleep", "infinity"],
            # Chrome 在默认 64MB /dev/shm 下会随机崩渲染进程
            shm_size="2g",
            dns=[DEFAULT_DNS],
            # Resolve the alias through the selected network, whose gateway may
            # differ from Docker's default bridge.
            extra_hosts={"host.docker.internal": gateway},
            network=DEFAULT_NETWORK,
            detach=True,
        )

    def _network_gateway(self):
        try:
            network = self.client.networks.get(DEFAULT_NETWORK)
            configs = network.attrs.get("IPAM", {}).get("Config", [])
        except Exception as exc:
            raise RuntimeError(
                f"cannot inspect Docker network {DEFAULT_NETWORK!r}: {exc}"
            ) from exc
        for config in configs:
            gateway = str(config.get("Gateway") or "").strip()
            try:
                if gateway and ipaddress.ip_address(gateway).version == 4:
                    return gateway
            except ValueError:
                continue
        raise RuntimeError(
            f"Docker network {DEFAULT_NETWORK!r} has no IPv4 IPAM gateway"
        )

    def _network_bridge_name(self):
        try:
            network = self.client.networks.get(DEFAULT_NETWORK)
            bridge = str(
                network.attrs.get("Options", {}).get(
                    "com.docker.network.bridge.name", ""
                )
            ).strip()
        except Exception as exc:
            raise RuntimeError(
                f"cannot inspect Docker network {DEFAULT_NETWORK!r}: {exc}"
            ) from exc
        if not bridge:
            raise RuntimeError(
                f"Docker network {DEFAULT_NETWORK!r} has no bridge name"
            )
        return bridge

    def _ensure_network_forwarding(self):
        bridge = self._network_bridge_name()
        rules = [
            ["-i", bridge, "-j", "ACCEPT"],
            [
                "-o",
                bridge,
                "-m",
                "conntrack",
                "--ctstate",
                "RELATED,ESTABLISHED",
                "-j",
                "ACCEPT",
            ],
        ]
        for rule in rules:
            check = subprocess.run(
                [
                    "sudo",
                    "-n",
                    "iptables",
                    "-C",
                    "DOCKER-FORWARD",
                    *rule,
                ],
                capture_output=True,
                text=True,
            )
            if check.returncode == 0:
                continue
            insert = subprocess.run(
                [
                    "sudo",
                    "-n",
                    "iptables",
                    "-I",
                    "DOCKER-FORWARD",
                    "1",
                    *rule,
                ],
                capture_output=True,
                text=True,
            )
            if insert.returncode != 0:
                raise RuntimeError(
                    "cannot restore Docker forwarding for "
                    f"{bridge}: {insert.stderr.strip() or insert.stdout.strip()}"
                )

    def get_ip_address(self, path_to_vm: str) -> str:
        if not all([self.server_port, self.chromium_port, self.vnc_port, self.vlc_port]):
            raise RuntimeError("Container not started - ports not allocated")
        return f"{self.container_ip}:{self.server_port}:{self.chromium_port}:{self.vnc_port}:{self.vlc_port}"

    def save_state(self, path_to_vm: str, snapshot_name: str):
        raise NotImplementedError("Snapshots not available for OCU provider")

    def revert_to_snapshot(self, path_to_vm: str, snapshot_name: str):
        """与官方 docker provider 同语义：没有快照，销毁容器即可，
        DesktopEnv 会在之后重新 start_emulator 得到一个全新的干净环境。"""
        self.stop_emulator(path_to_vm)
        return path_to_vm

    def stop_emulator(self, path_to_vm: str = None, region=None, *args, **kwargs):
        if self.container:
            logger.info(f"Stopping osw-hybrid container {self.container.short_id}...")
            try:
                self.container.stop(timeout=10)
                self.container.remove(v=True)
                time.sleep(WAIT_TIME)
            except Exception as e:
                logger.error(f"Error stopping container: {e}")
            finally:
                self.container = None
                self.server_port = None
                self.vnc_port = None
                self.chromium_port = None
                self.vlc_port = None

    def get_container_name(self) -> str:
        """给 harness 用：MCP 要通过 `docker exec -i <name> computer-use mcp`
        接进这个容器，所以必须能拿到容器标识。"""
        if not self.container:
            raise RuntimeError("Container not started")
        return self.container.name
